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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// An attempt's diagnostics answer "where is it stuck, and why did it fail" at
// a glance in the review popup (#5600). The runner keeps them current in
// attempts/<job>/diagnostics.json; like the rest of the record they are local
// and private.
const (
	reviewDiagnosticsFile = "diagnostics.json"
	reviewVersionTimeout  = 3 * time.Second
)

type reviewStageTime struct {
	Name  string    `json:"name"`
	Label string    `json:"label"`
	Start time.Time `json:"start"`
}

// reviewAttemptDiagnostics is one attempt's diagnostic snapshot.
type reviewAttemptDiagnostics struct {
	Attempt  string            `json:"attempt"`
	Provider string            `json:"provider"`
	Binary   string            `json:"binary,omitempty"`
	Version  string            `json:"version,omitempty"`
	Started  time.Time         `json:"started"`
	Stages   []reviewStageTime `json:"stages"`
	Deadline time.Time         `json:"deadline,omitzero"`
	Renewed  time.Time         `json:"lease_renewed,omitzero"`
	Finished time.Time         `json:"finished,omitzero"`
	Outcome  string            `json:"outcome,omitempty"`
	// Failure is the runner's execution diagnostic when the attempt failed.
	Failure *reviewExecutionDiagnostic `json:"failure,omitempty"`
}

// reviewHarnessVersion asks a harness binary for its version, briefly and
// without the user's environment beyond PATH and HOME.
func reviewHarnessVersion(binary string) string {
	if binary == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewVersionTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "vibeflow-harness-version-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Dir = dir // never the runner's or a checkout's directory
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return reviewEventText(first)
}

func saveReviewDiagnostics(dir string, d reviewAttemptDiagnostics) {
	if dir == "" {
		return
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil || os.MkdirAll(dir, 0700) != nil {
		return
	}
	tmp := filepath.Join(dir, reviewDiagnosticsFile+".tmp")
	if os.WriteFile(tmp, data, 0600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, reviewDiagnosticsFile))
	}
}

func readReviewDiagnostics(dir string) (reviewAttemptDiagnostics, bool) {
	var d reviewAttemptDiagnostics
	if dir == "" {
		return d, false
	}
	data, err := os.ReadFile(filepath.Join(dir, reviewDiagnosticsFile))
	if err != nil || json.Unmarshal(data, &d) != nil {
		return reviewAttemptDiagnostics{}, false
	}
	return d, true
}

// reviewDiagnosticsLines renders diagnostics for the popup at time now.
func reviewDiagnosticsLines(d reviewAttemptDiagnostics, now time.Time) []string {
	text := reviewEventText
	end := now
	if !d.Finished.IsZero() {
		end = d.Finished
	}
	harness := text(d.Provider)
	if d.Version != "" {
		harness += " " + text(d.Version)
	}
	if d.Binary != "" {
		harness += " · " + text(d.Binary)
	}
	lines := []string{"Harness: " + harness}
	attempt := fmt.Sprintf("Attempt: %s · started %s", text(d.Attempt), d.Started.Local().Format("15:04:05"))
	if d.Finished.IsZero() {
		attempt += " · " + roundElapsed(now.Sub(d.Started)) + " elapsed"
	} else {
		attempt += " · finished after " + roundElapsed(d.Finished.Sub(d.Started)) + ": " + text(d.Outcome)
	}
	lines = append(lines, attempt)
	if n := len(d.Stages); n > 0 {
		last := d.Stages[n-1]
		if d.Finished.IsZero() {
			lines = append(lines, fmt.Sprintf("Stage: %s for %s", text(last.Label), roundElapsed(now.Sub(last.Start))))
		}
		var parts []string
		for i, s := range d.Stages {
			stop := end
			if i+1 < n {
				stop = d.Stages[i+1].Start
			}
			parts = append(parts, text(s.Label)+" "+roundElapsed(stop.Sub(s.Start)))
		}
		lines = append(lines, "Stages: "+strings.Join(parts, " · "))
	}
	var lease []string
	if !d.Deadline.IsZero() {
		left := "past"
		if l := d.Deadline.Sub(now); l > 0 {
			left = roundElapsed(l) + " left"
		}
		lease = append(lease, fmt.Sprintf("deadline %s (%s)", d.Deadline.Local().Format("15:04"), left))
	}
	if !d.Renewed.IsZero() {
		lease = append(lease, "lease renewed "+d.Renewed.Local().Format("15:04:05"))
	}
	if len(lease) > 0 {
		lines = append(lines, "Lease: "+strings.Join(lease, " · "))
	}
	if f := d.Failure; f != nil {
		failure := fmt.Sprintf("Failure: %s at %s", text(f.Category), text(f.Stage))
		if f.Reason != "" {
			failure += " · " + text(f.Reason)
		}
		if f.ExitCode != nil {
			failure += fmt.Sprintf(" · exit %d", *f.ExitCode)
		}
		if f.Signal != 0 {
			failure += fmt.Sprintf(" · signal %d", f.Signal)
		}
		if f.APIErrorStatus != 0 {
			failure += fmt.Sprintf(" · API HTTP %d", f.APIErrorStatus)
		}
		failure += fmt.Sprintf(" · harness output %d B stdout, %d B stderr", f.StdoutBytes, f.StderrBytes)
		lines = append(lines, failure)
	}
	return lines
}
