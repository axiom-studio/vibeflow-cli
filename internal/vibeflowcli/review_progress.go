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
	attempt  string // the server's attempt ID, named in the event log
	binary   string // the harness binary's resolved path, for diagnostics
	claim    string // the claim call's event line, logged before the reporter existed
	provider string
	started  time.Time
	deadline func() time.Time

	mu       sync.Mutex
	snapshot string // the pane as last seen while the attempt ran
	version  string // the harness's --version, probed once at start
	stages   []reviewStageTime
	failure  *reviewExecutionDiagnostic
	finished time.Time
	outcome  string
	leaseExp time.Time // the lease's current expiry
	exitCode *int      // the harness's exit status, once it has exited
	signal   int
	stage    string
	renewed  time.Time
	logged   time.Time
	stop     chan struct{}
	done     chan struct{}
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
	// Each attempt starts its review's event log afresh.
	if dir := reviewAttemptDir(r.logPath, r.jobID); dir != "" && os.MkdirAll(dir, 0700) == nil {
		_ = os.WriteFile(filepath.Join(dir, reviewEventsFile), nil, 0600)
	}
	r.event(fmt.Sprintf("attempt %s started with %s", reviewEventText(r.attempt), r.provider))
	if r.claim != "" {
		r.event(r.claim) // the claim ran before this attempt's reporter existed (#5602)
	}
	r.mu.Lock()
	r.stages = []reviewStageTime{{Name: r.stage, Label: reviewStageLabel(r.stage, r.provider), Start: r.started}}
	r.mu.Unlock()
	if r.binary != "" {
		go func() {
			v := reviewHarnessVersion(r.binary)
			r.mu.Lock()
			r.version = v
			r.mu.Unlock()
			r.writeDiagnostics()
		}()
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
	if changed {
		r.stages = append(r.stages, reviewStageTime{Name: stage, Label: reviewStageLabel(stage, r.provider), Start: time.Now()})
	}
	r.mu.Unlock()
	if changed {
		r.event("stage " + stage + ": " + reviewStageLabel(stage, r.provider))
		r.refresh(true)
	}
}

// event appends one timestamped line to the review's event log (#5602).
func (r *reviewAttemptProgress) event(line string) {
	if r == nil {
		return
	}
	dir := reviewAttemptDir(r.logPath, r.jobID)
	if dir == "" {
		return
	}
	path := filepath.Join(dir, reviewEventsFile)
	r.mu.Lock() // server calls arrive from several goroutines
	defer r.mu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(time.Now().Format("15:04:05.000") + " " + line + "\n")
	info, statErr := f.Stat()
	f.Close()
	if statErr == nil && info.Size() > reviewEventsMax {
		trimLogLines(path, reviewEventsKeep)
	}
}

// requestEvent logs one review API call.
func (r *reviewAttemptProgress) requestEvent(method, path string, status int, took time.Duration, err error) {
	if r == nil {
		return
	}
	r.event(reviewRequestEventLine(method, path, status, took, err))
}

// reviewRequestEventLine describes one review API call: method, path
// template (query dropped, IDs replaced), status or why there was none, and
// latency.
func reviewRequestEventLine(method, path string, status int, took time.Duration, err error) string {
	path, _, _ = strings.Cut(path, "?")
	result := fmt.Sprintf("%d", status)
	if status == 0 && err != nil {
		result = "no response: " + reviewEventText(err.Error())
	}
	return fmt.Sprintf("%s %s → %s in %dms", reviewEventText(method), reviewEventText(reviewPathTemplate(path)), result, took.Milliseconds())
}

// reviewPathTemplate replaces the runner, job and attempt IDs in a review API
// path with placeholders, so event lines read as the call they are:
// /projects/1/pr-review-runners/{runner}/jobs/{job}/attempts/{attempt}/renew.
func reviewPathTemplate(path string) string {
	placeholders := map[string]string{"pr-review-runners": "{runner}", "jobs": "{job}", "attempts": "{attempt}"}
	parts := strings.Split(path, "/")
	for i := 0; i+1 < len(parts); i++ {
		if ph, ok := placeholders[parts[i]]; ok && parts[i+1] != "" {
			parts[i+1] = ph
			i++
		}
	}
	return strings.Join(parts, "/")
}

// reviewEventText keeps an event field on one printable line of at most 300
// runes; API paths are longer than a pane title, so no tighter clamp.
func reviewEventText(s string) string {
	s = strings.Join(reviewDetailText(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// setFailure records the runner's execution diagnostic for a failed attempt.
func (r *reviewAttemptProgress) setFailure(d reviewExecutionDiagnostic) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.failure = &d
	r.mu.Unlock()
	r.writeDiagnostics()
}

// writeDiagnostics saves the attempt's current diagnostics (#5600).
func (r *reviewAttemptProgress) writeDiagnostics() {
	dir := reviewAttemptDir(r.logPath, r.jobID)
	if dir == "" {
		return
	}
	var deadline time.Time
	if r.deadline != nil {
		deadline = r.deadline()
	}
	r.mu.Lock()
	d := reviewAttemptDiagnostics{
		Attempt: r.attempt, Provider: r.provider, Binary: r.binary, Version: r.version,
		Started: r.started, Stages: append([]reviewStageTime(nil), r.stages...), Deadline: deadline,
		Renewed: r.renewed, LeaseExpires: r.leaseExp, ExitCode: r.exitCode, Signal: r.signal,
		Finished: r.finished, Outcome: r.outcome, Failure: r.failure,
	}
	saveReviewDiagnostics(dir, d)
	r.mu.Unlock() // held across the write so snapshots land in order
}

// leaseRenewed records a successful lease renewal and the expiry it set.
func (r *reviewAttemptProgress) leaseRenewed(at, expires time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.renewed, r.leaseExp = at, expires
	r.mu.Unlock()
}

// leaseExpires records the lease expiry the claim granted.
func (r *reviewAttemptProgress) leaseExpires(expires time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.leaseExp = expires
	r.mu.Unlock()
}

// harnessExit records the harness's exit status.
func (r *reviewAttemptProgress) harnessExit(code *int, signal int) {
	if r == nil || (code == nil && signal == 0) {
		return
	}
	r.mu.Lock()
	r.exitCode, r.signal = code, signal
	r.mu.Unlock()
}

// capturePane reads the slot pane's screen and scrollback as plain text.
func (r *reviewAttemptProgress) capturePane() string {
	if r == nil || r.run == nil || r.pane == "" {
		return ""
	}
	out, err := r.run("capture-pane", "-p", "-J", "-t", r.pane, "-S", fmt.Sprintf("-%d", reviewTranscriptLines))
	if err != nil {
		return ""
	}
	return out
}

// saveTranscript keeps the attempt's harness output once the harness has
// exited (#5601): the slot pane's screen and scrollback, or the longer copy
// seen while it ran (a harness on the alternate screen leaves nothing behind
// when it exits), or, without a pane, fallback (a headless harness's output
// tail). Best effort, like the log.
func (r *reviewAttemptProgress) saveTranscript(fallback string) {
	if r == nil {
		return
	}
	text := r.capturePane()
	r.mu.Lock()
	if len(r.snapshot) > len(text) {
		text = r.snapshot
	}
	r.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		text = fallback
	}
	_ = saveReviewTranscript(reviewAttemptDir(r.logPath, r.jobID), text)
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
	r.event("finished: " + outcome)
	r.mu.Lock()
	r.finished, r.outcome = time.Now(), outcome
	r.mu.Unlock()
	r.writeDiagnostics()
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
	if snap := r.capturePane(); snap != "" {
		r.mu.Lock()
		r.snapshot = snap
		r.mu.Unlock()
	}
	if r.run != nil && r.pane != "" {
		// The border inserts the option's value literally: tmux does not
		// re-expand it as a format.
		_, _ = r.run("set-option", "-p", "-t", r.pane, reviewProgressOption, line)
	}
	r.writeDiagnostics()
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
func trimReviewProgressLog(path string) { trimLogLines(path, reviewProgressLogKeep) }

// trimLogLines keeps a log file's newest keep lines.
func trimLogLines(path string, keep int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= keep {
		return
	}
	kept := strings.Join(lines[len(lines)-keep:], "\n") + "\n"
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
