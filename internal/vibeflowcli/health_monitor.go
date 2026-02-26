/*
 * Copyright (c) 2026. AXIOM STUDIO AI Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package vibeflowcli

import (
	"strings"
	"time"
)

// HealthStatus represents the current health state of a monitored session.
type HealthStatus int

const (
	HealthHealthy       HealthStatus = iota
	HealthErrorDetected              // Error matched but debouncing before recovery.
	HealthRecovering                 // Recovery message sent, waiting for effect.
	HealthFailed                     // Max retries exceeded — manual intervention needed.
)

// String returns a human-readable label for the health status.
func (s HealthStatus) String() string {
	switch s {
	case HealthHealthy:
		return "healthy"
	case HealthErrorDetected:
		return "error_detected"
	case HealthRecovering:
		return "recovering"
	case HealthFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// SessionHealth tracks health and recovery state for a single session.
type SessionHealth struct {
	SessionName    string
	Provider       string
	Status         HealthStatus
	LastErrorAt    time.Time
	MatchedPattern *ErrorPattern
	RecoveryCount  int
	LastRecoveryAt time.Time
	BackoffUntil   time.Time
	LastOutput     string // previous capture output for change detection
}

// HealthMonitor manages health state for all active sessions and coordinates
// error detection + auto-recovery via SendKeys.
type HealthMonitor struct {
	sessions map[string]*SessionHealth // keyed by tmux session name
	registry *ErrorPatternRegistry
	tmux     *TmuxManager
	config   ErrorRecoveryConfig
	logger   *Logger
}

// NewHealthMonitor creates a health monitor wired to the given dependencies.
func NewHealthMonitor(registry *ErrorPatternRegistry, tmux *TmuxManager, cfg ErrorRecoveryConfig, logger *Logger) *HealthMonitor {
	return &HealthMonitor{
		sessions: make(map[string]*SessionHealth),
		registry: registry,
		tmux:     tmux,
		config:   cfg,
		logger:   logger,
	}
}

// CheckOutput scans captured pane output for a session and updates health state.
// Only the last few lines of output are checked to avoid false positives from
// error strings appearing in code discussions.
// Returns true if a recovery attempt should be triggered.
func (hm *HealthMonitor) CheckOutput(sessionName, provider, output string, isAttached bool) bool {
	if !hm.config.Enabled {
		return false
	}

	sh := hm.getOrCreate(sessionName, provider)

	// If session has failed, don't do anything further (manual intervention needed).
	if sh.Status == HealthFailed {
		return false
	}

	// Only scan the last 10 lines of output for error patterns.
	tail := lastNLines(output, 10)
	match := hm.registry.Match(provider, tail)

	if match == nil {
		// No error — if we were in error_detected or recovering, the issue resolved.
		if sh.Status == HealthErrorDetected || sh.Status == HealthRecovering {
			hm.logger.Info("health: session %s recovered (was %s)", sessionName, sh.Status)
			sh.Status = HealthHealthy
			sh.RecoveryCount = 0
			sh.MatchedPattern = nil
		}
		sh.LastOutput = output
		return false
	}

	// Fatal errors — mark failed immediately, no recovery.
	if match.Severity == SeverityFatal {
		sh.Status = HealthFailed
		sh.MatchedPattern = match
		sh.LastErrorAt = time.Now()
		hm.logger.Warn("health: session %s fatal error: %s", sessionName, match.Description)
		return false
	}

	// Recoverable error detected.
	now := time.Now()

	switch sh.Status {
	case HealthHealthy:
		// First detection — start debounce.
		sh.Status = HealthErrorDetected
		sh.LastErrorAt = now
		sh.MatchedPattern = match
		sh.LastOutput = output
		hm.logger.Info("health: session %s error detected: %s (debouncing)", sessionName, match.Description)
		return false

	case HealthErrorDetected:
		// Debounce check: has enough time passed since first detection?
		debounce := time.Duration(hm.config.DebounceSeconds) * time.Second
		if now.Sub(sh.LastErrorAt) < debounce {
			return false // Still debouncing.
		}
		// Check output hasn't changed (no new activity).
		if output != sh.LastOutput {
			// Output changed — reset debounce, session may be recovering on its own.
			sh.LastErrorAt = now
			sh.LastOutput = output
			return false
		}
		// Debounce passed, output unchanged — trigger recovery if not attached.
		if isAttached {
			return false // User is interacting, don't inject.
		}
		return hm.shouldRecover(sh)

	case HealthRecovering:
		// Check if we're still in backoff.
		if now.Before(sh.BackoffUntil) {
			return false
		}
		// Output unchanged after recovery attempt — error persists, try again.
		if output == sh.LastOutput {
			if isAttached {
				return false
			}
			return hm.shouldRecover(sh)
		}
		// Output changed — might be recovering, reset to error_detected for fresh debounce.
		sh.Status = HealthErrorDetected
		sh.LastErrorAt = now
		sh.LastOutput = output
		return false
	}

	sh.LastOutput = output
	return false
}

// AttemptRecovery sends the recovery message for a session and updates state.
func (hm *HealthMonitor) AttemptRecovery(sessionName string) error {
	sh, ok := hm.sessions[sessionName]
	if !ok || sh.MatchedPattern == nil {
		return nil
	}

	msg := sh.MatchedPattern.RecoveryMessage
	if msg == "" {
		return nil
	}

	hm.logger.Info("health: session %s recovery attempt %d/%d: sending '%s'",
		sessionName, sh.RecoveryCount+1, hm.config.MaxRetries, truncateLog(msg, 60))

	if err := hm.tmux.SendKeys(sessionName, msg); err != nil {
		hm.logger.Error("health: session %s send-keys failed: %v", sessionName, err)
		return err
	}

	sh.RecoveryCount++
	sh.LastRecoveryAt = time.Now()
	sh.Status = HealthRecovering

	// Calculate exponential backoff for next attempt.
	backoffBase := 30 * time.Second
	multiplier := hm.config.BackoffMultiplier
	if multiplier < 1 {
		multiplier = 2
	}
	backoff := backoffBase
	for i := 1; i < sh.RecoveryCount; i++ {
		backoff *= time.Duration(multiplier)
	}
	sh.BackoffUntil = sh.LastRecoveryAt.Add(backoff)

	// Check if max retries exceeded.
	if sh.RecoveryCount >= hm.config.MaxRetries {
		sh.Status = HealthFailed
		hm.logger.Warn("health: session %s failed after %d recovery attempts", sessionName, sh.RecoveryCount)
	}

	return nil
}

// ResetSession resets health state for a session (e.g. after manual retry).
func (hm *HealthMonitor) ResetSession(sessionName string) {
	if sh, ok := hm.sessions[sessionName]; ok {
		sh.Status = HealthHealthy
		sh.RecoveryCount = 0
		sh.MatchedPattern = nil
		sh.BackoffUntil = time.Time{}
	}
}

// GetHealth returns the health state for a session, or nil if not tracked.
func (hm *HealthMonitor) GetHealth(sessionName string) *SessionHealth {
	return hm.sessions[sessionName]
}

// RemoveSession removes health tracking for a killed session.
func (hm *HealthMonitor) RemoveSession(sessionName string) {
	delete(hm.sessions, sessionName)
}

func (hm *HealthMonitor) getOrCreate(sessionName, provider string) *SessionHealth {
	if sh, ok := hm.sessions[sessionName]; ok {
		return sh
	}
	sh := &SessionHealth{
		SessionName: sessionName,
		Provider:    provider,
		Status:      HealthHealthy,
	}
	hm.sessions[sessionName] = sh
	return sh
}

func (hm *HealthMonitor) shouldRecover(sh *SessionHealth) bool {
	if sh.RecoveryCount >= hm.config.MaxRetries {
		sh.Status = HealthFailed
		hm.logger.Warn("health: session %s max retries reached (%d)", sh.SessionName, hm.config.MaxRetries)
		return false
	}
	return true
}

func lastNLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
