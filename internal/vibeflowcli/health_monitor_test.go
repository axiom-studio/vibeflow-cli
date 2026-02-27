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
	"os"
	"path/filepath"
	"testing"
)


func testHealthMonitor(t *testing.T) *HealthMonitor {
	t.Helper()
	reg := NewErrorPatternRegistry()
	// Use a real TmuxManager but we won't call methods that need a tmux server.
	tmux := &TmuxManager{socketName: "test"}
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "test.log")
	// Create a minimal logger.
	f, _ := os.Create(logPath)
	logger := &Logger{file: f, path: logPath}
	cfg := ErrorRecoveryConfig{
		Enabled:           true,
		MaxRetries:        3,
		DebounceSeconds:   0, // No debounce for tests.
		BackoffMultiplier: 2,
	}
	return NewHealthMonitor(reg, tmux, cfg, logger)
}

func TestNewHealthMonitor(t *testing.T) {
	hm := testHealthMonitor(t)
	if hm == nil {
		t.Fatal("expected non-nil monitor")
	}
	if len(hm.sessions) != 0 {
		t.Error("expected empty sessions map")
	}
}

func TestHealthMonitor_CheckOutput_NoError(t *testing.T) {
	hm := testHealthMonitor(t)
	shouldRecover := hm.CheckOutput("vibeflow_test", "claude", "All good\nNo errors", false)
	if shouldRecover {
		t.Error("should not trigger recovery for clean output")
	}

	sh := hm.GetHealth("vibeflow_test")
	if sh == nil {
		t.Fatal("expected session health to be created")
	}
	if sh.Status != HealthHealthy {
		t.Errorf("expected healthy, got %s", sh.Status)
	}
}

func TestHealthMonitor_CheckOutput_FatalError(t *testing.T) {
	hm := testHealthMonitor(t)
	shouldRecover := hm.CheckOutput("vibeflow_test", "claude", "panic: runtime error", false)
	if shouldRecover {
		t.Error("should not trigger recovery for fatal error")
	}

	sh := hm.GetHealth("vibeflow_test")
	if sh.Status != HealthFailed {
		t.Errorf("expected failed, got %s", sh.Status)
	}
}

func TestHealthMonitor_CheckOutput_RecoverableError(t *testing.T) {
	hm := testHealthMonitor(t)

	// First check: error detected, starts debouncing.
	output := "Some output\nAPI Error: 500\nmore text"
	shouldRecover := hm.CheckOutput("vibeflow_test", "claude", output, false)
	if shouldRecover {
		t.Error("first detection should start debounce, not trigger recovery")
	}

	sh := hm.GetHealth("vibeflow_test")
	if sh.Status != HealthErrorDetected {
		t.Errorf("expected error_detected, got %s", sh.Status)
	}

	// Second check with same output (debounce passed since DebounceSeconds=0):
	// should trigger recovery.
	shouldRecover = hm.CheckOutput("vibeflow_test", "claude", output, false)
	if !shouldRecover {
		t.Error("expected recovery trigger after debounce")
	}
}

func TestHealthMonitor_CheckOutput_Disabled(t *testing.T) {
	hm := testHealthMonitor(t)
	hm.config.Enabled = false

	shouldRecover := hm.CheckOutput("vibeflow_test", "claude", "API Error: 500", false)
	if shouldRecover {
		t.Error("should not trigger when disabled")
	}
}

func TestHealthMonitor_CheckOutput_Attached(t *testing.T) {
	hm := testHealthMonitor(t)
	output := "API Error: 500"

	// First check: error detected.
	hm.CheckOutput("vibeflow_test", "claude", output, false)

	// Second check while attached: should NOT trigger recovery.
	shouldRecover := hm.CheckOutput("vibeflow_test", "claude", output, true)
	if shouldRecover {
		t.Error("should not trigger recovery when session is attached")
	}
}

func TestHealthMonitor_CheckOutput_MaxRetries(t *testing.T) {
	hm := testHealthMonitor(t)
	hm.config.MaxRetries = 2

	output := "API Error: 500"

	// First: error detected.
	hm.CheckOutput("vibeflow_test", "claude", output, false)

	// Second: should recover.
	shouldRecover := hm.CheckOutput("vibeflow_test", "claude", output, false)
	if !shouldRecover {
		t.Fatal("expected recovery")
	}

	// Simulate recovery count increase (normally done by AttemptRecovery).
	sh := hm.GetHealth("vibeflow_test")
	sh.RecoveryCount = 2

	// Next check: should fail (max retries).
	shouldRecover = hm.CheckOutput("vibeflow_test", "claude", output, false)
	if shouldRecover {
		t.Error("should not trigger after max retries")
	}
	if sh.Status != HealthFailed {
		t.Errorf("expected failed status, got %s", sh.Status)
	}
}

func TestHealthMonitor_CheckOutput_Recovery(t *testing.T) {
	hm := testHealthMonitor(t)
	errorOutput := "API Error: 500"

	// Detection + debounce pass.
	hm.CheckOutput("vibeflow_test", "claude", errorOutput, false)
	hm.CheckOutput("vibeflow_test", "claude", errorOutput, false)

	// Now check with different output (recovery happened).
	cleanOutput := "All good now"
	hm.CheckOutput("vibeflow_test", "claude", cleanOutput, false)

	sh := hm.GetHealth("vibeflow_test")
	if sh.Status != HealthHealthy {
		t.Errorf("expected healthy after recovery, got %s", sh.Status)
	}
}

func TestHealthMonitor_ResetSession(t *testing.T) {
	hm := testHealthMonitor(t)

	// Create a failed session.
	hm.CheckOutput("vibeflow_test", "claude", "panic: fatal", false)
	sh := hm.GetHealth("vibeflow_test")
	if sh.Status != HealthFailed {
		t.Fatal("expected failed")
	}

	hm.ResetSession("vibeflow_test")
	sh = hm.GetHealth("vibeflow_test")
	if sh.Status != HealthHealthy {
		t.Errorf("expected healthy after reset, got %s", sh.Status)
	}
	if sh.RecoveryCount != 0 {
		t.Errorf("expected 0 recovery count, got %d", sh.RecoveryCount)
	}
}

func TestHealthMonitor_RemoveSession(t *testing.T) {
	hm := testHealthMonitor(t)
	hm.CheckOutput("vibeflow_test", "claude", "something", false)
	if hm.GetHealth("vibeflow_test") == nil {
		t.Fatal("expected session to exist")
	}

	hm.RemoveSession("vibeflow_test")
	if hm.GetHealth("vibeflow_test") != nil {
		t.Error("expected session to be removed")
	}
}

func TestHealthMonitor_GetHealth_NotTracked(t *testing.T) {
	hm := testHealthMonitor(t)
	if hm.GetHealth("nonexistent") != nil {
		t.Error("expected nil for untracked session")
	}
}

func TestHealthStatus_String(t *testing.T) {
	tests := []struct {
		status   HealthStatus
		expected string
	}{
		{HealthHealthy, "healthy"},
		{HealthErrorDetected, "error_detected"},
		{HealthRecovering, "recovering"},
		{HealthFailed, "failed"},
		{HealthStatus(99), "unknown"},
	}

	for _, tc := range tests {
		got := tc.status.String()
		if got != tc.expected {
			t.Errorf("HealthStatus(%d).String() = %q, want %q", tc.status, got, tc.expected)
		}
	}
}
