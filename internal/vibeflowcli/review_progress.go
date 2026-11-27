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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A running review used to be silent from "Starting claude" until its result,
// which can be most of half an hour (#5598). While the harness owns the slot
// pane, printing there would corrupt its screen, so progress goes to two
// places instead: a line on the slot pane's top border (refreshed often) and
// a timestamped log shared by the console's slots, shown in the history pane.
const (
	reviewProgressRefresh = 10 * time.Second // border line
	reviewProgressLogged  = time.Minute      // periodic log line
	reviewProgressLogMax  = 64 << 10         // the log is trimmed past this size...
	reviewProgressLogKeep = 200              // ...to its newest lines
	reviewProgressOption  = "@vibeflow_vera_progress"
	// reviewLiveJobOption names, on a slot pane, the review job running in it,
	// so that review's popup can show the harness's live output (#5599).
	reviewLiveJobOption = "@vibeflow_vera_job"
)

// reviewProgressLogPath is the console's progress log. Every slot of one Vera
// console and its history pane derive the same path: a slot's options are the
// console's plus its label, so the label is removed first.
func reviewProgressLogPath(serverURL string, o reviewWatchOptions) string {
	if o.SlotLabel != "" {
		o.Name = strings.TrimSuffix(o.Name, " · "+o.SlotLabel)
		o.SlotKind, o.SlotLabel = "", ""
	}
	return filepath.Join(RootDir(), "review-runners", reviewBackgroundID(serverURL, o), "progress.log")
}

// reviewStageLabel says in plain words what an attempt stage is doing.
func reviewStageLabel(stage, provider string) string {
	switch stage {
	case "brief":
		return "preparing the brief"
	case "checkout":
		return "checking out the PR"
	case "provider_setup":
		return "preparing " + provider
	case "child_guard", "provider":
		return provider + " working"
	case "result":
		return "reading the result"
	case "findings":
		return "checking findings"
	}
	return stage
}

// reviewAttemptProgress reports one attempt's progress while it runs. All methods
// are safe on a nil receiver, so callers outside a console slot pass nil.
type reviewAttemptProgress struct {
	run      veraTmux // nil outside tmux: no border line
	pane     string
	logPath  string
	slot     string // "review 1"
	job      string // "review #49"
	jobID    string // the server's job ID; tags the pane for the review popup
	provider string
	started  time.Time
	deadline func() time.Time

	mu      sync.Mutex
	stage   string
	renewed time.Time
	logged  time.Time
	stop    chan struct{}
	done    chan struct{}
}

func newReviewAttemptProgress(run veraTmux, pane, logPath, slot, job, provider string, deadline func() time.Time) *reviewAttemptProgress {
	// job can fall back to a server-supplied review ID; keep only printable
	// runes before it reaches the tmux border or the terminal.
	return &reviewAttemptProgress{run: run, pane: pane, logPath: logPath, slot: slot, job: sanitizeWorkbenchTitle(job), provider: provider,
		started: time.Now(), deadline: deadline, stage: "brief"}
}

// start shows the border line and keeps it, and the log, current until finish.
func (r *reviewAttemptProgress) start() {
	if r == nil {
		return
	}
	if r.run != nil && r.pane != "" {
		_, _ = r.run("set-option", "-w", "-t", r.pane, "pane-border-status", "top")
		_, _ = r.run("set-option", "-w", "-t", r.pane, "pane-border-format", " #{"+reviewProgressOption+"} ")
		if reviewPublicID(r.jobID) {
			_, _ = r.run("set-option", "-p", "-t", r.pane, reviewLiveJobOption, r.jobID)
		}
	}
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	r.refresh(true)
	go func() {
		defer close(r.done)
		tick := time.NewTicker(reviewProgressRefresh)
		defer tick.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-tick.C:
				r.refresh(false)
			}
		}
	}()
}

// setStage records a new attempt stage and logs it.
func (r *reviewAttemptProgress) setStage(stage string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	changed := r.stage != stage
	r.stage = stage
	r.mu.Unlock()
	if changed {
		r.refresh(true)
	}
}

// leaseRenewed records a successful lease renewal.
func (r *reviewAttemptProgress) leaseRenewed(at time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.renewed = at
	r.mu.Unlock()
}

// finish logs the outcome and removes the border line.
func (r *reviewAttemptProgress) finish(outcome string) {
	if r == nil {
		return
	}
	if r.stop != nil {
		close(r.stop)
		<-r.done
	}
	r.appendLog(fmt.Sprintf("%s %s · %s · %s after %s", time.Now().Format("15:04"), r.slot, r.job, outcome, roundElapsed(time.Since(r.started))))
	if r.run != nil && r.pane != "" {
		_, _ = r.run("set-option", "-w", "-u", "-t", r.pane, "pane-border-status")
		_, _ = r.run("set-option", "-w", "-u", "-t", r.pane, "pane-border-format")
		_, _ = r.run("set-option", "-p", "-u", "-t", r.pane, reviewProgressOption)
		_, _ = r.run("set-option", "-p", "-u", "-t", r.pane, reviewLiveJobOption)
	}
}

// line is the current progress, e.g. "Vera · review #49 · claude working ·
// 6m elapsed · 23m left · lease renewed 08:01".
func (r *reviewAttemptProgress) line(now time.Time) string {
	r.mu.Lock()
	stage, renewed := r.stage, r.renewed
	r.mu.Unlock()
	parts := []string{"Vera", r.job, reviewStageLabel(stage, r.provider), roundElapsed(now.Sub(r.started)) + " elapsed"}
	if r.deadline != nil {
		if left := r.deadline().Sub(now); left > 0 {
			parts = append(parts, roundElapsed(left)+" left")
		} else {
			parts = append(parts, "past its deadline")
		}
	}
	if !renewed.IsZero() {
		parts = append(parts, "lease renewed "+renewed.Format("15:04"))
	}
	return strings.Join(parts, " · ")
}

func (r *reviewAttemptProgress) refresh(logNow bool) {
	now := time.Now()
	line := r.line(now)
	if r.run != nil && r.pane != "" {
		// The border inserts the option's value literally: tmux does not
		// re-expand it as a format.
		_, _ = r.run("set-option", "-p", "-t", r.pane, reviewProgressOption, line)
	}
	r.mu.Lock()
	due := logNow || now.Sub(r.logged) >= reviewProgressLogged
	if due {
		r.logged = now
	}
	r.mu.Unlock()
	if due {
		r.appendLog(now.Format("15:04") + " " + r.slot + " · " + strings.TrimPrefix(line, "Vera · "))
	}
}

// appendLog adds one line to the console's progress log, keeping it bounded.
// Logging is best effort: a review never fails because its log could not be
// written.
func (r *reviewAttemptProgress) appendLog(line string) {
	if r == nil || r.logPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.logPath), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(r.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	info, statErr := f.Stat()
	f.Close()
	if statErr == nil && info.Size() > reviewProgressLogMax {
		trimReviewProgressLog(r.logPath)
	}
}

// trimReviewProgressLog keeps the newest reviewProgressLogKeep lines.
func trimReviewProgressLog(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= reviewProgressLogKeep {
		return
	}
	kept := strings.Join(lines[len(lines)-reviewProgressLogKeep:], "\n") + "\n"
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(kept), 0600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// readReviewProgressLog returns up to n of the newest log lines, oldest first.
func readReviewProgressLog(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil || n <= 0 {
		return nil
	}
	data = bytes.TrimRight(data, "\n")
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	return lines[max(0, len(lines)-n):]
}

// roundElapsed prints durations the way a person reads them: seconds under a
// minute, then minutes, then hours and minutes.
func roundElapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
