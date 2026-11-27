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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestReviewTranscript_SavedPrivateSanitizedAndBounded(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "console", "progress.log")
	dir := reviewAttemptDir(logPath, "job-4")
	if err := saveReviewTranscript(dir, "\x1b[31mAPI Error: 403\x1b[0m\x07\nPlease run /login\n"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("record dir mode: %v %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(dir, reviewTranscriptFile)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("transcript mode: %v %v", info, err)
	}
	lines, saved, ok := readReviewTranscript(dir)
	if !ok || time.Since(saved) > time.Minute || strings.Join(lines, "\n") != "API Error: 403\nPlease run /login" {
		t.Fatalf("read back %q (%v %v)", lines, saved, ok)
	}

	// A huge transcript keeps its newest whole lines within the cap.
	var big strings.Builder
	for i := 0; big.Len() < 2*reviewTranscriptMax; i++ {
		fmt.Fprintf(&big, "line %07d of harness output\n", i)
	}
	if err := saveReviewTranscript(dir, big.String()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, reviewTranscriptFile))
	if len(data) > reviewTranscriptMax+len(reviewTranscriptHeader)+1 {
		t.Errorf("transcript is %d bytes, cap %d", len(data), reviewTranscriptMax)
	}
	lines, _, _ = readReviewTranscript(dir)
	if !strings.HasPrefix(lines[0], "line ") || !strings.HasSuffix(strings.TrimSpace(big.String()), lines[len(lines)-1]) {
		t.Errorf("truncation must keep whole newest lines: first %q last %q", lines[0], lines[len(lines)-1])
	}

	// An unsafe job ID never becomes a path.
	if reviewAttemptDir(logPath, "../etc") != "" || reviewAttemptDir(logPath, "") != "" || reviewAttemptDir("", "job-4") != "" {
		t.Error("unsafe record path accepted")
	}
	if err := saveReviewTranscript("", "x"); err != nil {
		t.Error(err)
	}
}

func TestReviewTranscript_PrunesToTheNewestRecords(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "progress.log")
	base := time.Now().Add(-time.Hour)
	for i := 0; i < reviewAttemptKeep+3; i++ {
		dir := reviewAttemptDir(logPath, fmt.Sprintf("job-%02d", i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		at := base.Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(dir, at, at)
	}
	stray := filepath.Join(filepath.Dir(logPath), reviewAttemptsDirName, "not a job id")
	_ = os.MkdirAll(stray, 0700)
	if err := saveReviewTranscript(reviewAttemptDir(logPath, "job-new"), "x"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(stray))
	jobs := 0
	for _, e := range entries {
		if reviewPublicID(e.Name()) {
			jobs++
		}
	}
	if jobs != reviewAttemptKeep {
		t.Errorf("kept %d records, want %d", jobs, reviewAttemptKeep)
	}
	for _, gone := range []string{"job-00", "job-01", "job-02", "job-03"} {
		if _, err := os.Stat(reviewAttemptDir(logPath, gone)); err == nil {
			t.Errorf("oldest record %s not pruned", gone)
		}
	}
	if _, err := os.Stat(reviewAttemptDir(logPath, "job-new")); err != nil {
		t.Error("the newest record was pruned")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("pruning must not touch a directory that is not a job record")
	}
}

// The runner keeps the slot pane's screen and scrollback when the harness
// exits; without a pane it keeps the headless output tail; a longer copy seen
// while running wins over a short final screen.
func TestReviewAttemptProgress_SavesTheTranscript(t *testing.T) {
	tm, pane, logPath := liveFixture(t, "")
	r := newReviewAttemptProgress(tm.run, pane, logPath, "review 1", "review #4", "claude", nil)
	r.jobID = "job-4"
	r.saveTranscript("headless tail")
	lines, _, ok := readReviewTranscript(reviewAttemptDir(logPath, "job-4"))
	if text := strings.Join(lines, "\n"); !ok || !strings.Contains(text, "claude step 1") || !strings.Contains(text, "red done") || strings.Contains(text, "headless tail") {
		t.Errorf("pane transcript:\n%s", text)
	}

	headless := newReviewAttemptProgress(nil, "", logPath, "answer 1", "answer #5", "codex", nil)
	headless.jobID = "job-5"
	headless.saveTranscript("codex: 401 Unauthorized")
	if lines, _, _ := readReviewTranscript(reviewAttemptDir(logPath, "job-5")); strings.Join(lines, "\n") != "codex: 401 Unauthorized" {
		t.Errorf("headless transcript: %q", lines)
	}

	headless.snapshot = "the harness screen as seen while running\nwith two lines"
	headless.saveTranscript("short")
	if lines, _, _ := readReviewTranscript(reviewAttemptDir(logPath, "job-5")); !strings.Contains(strings.Join(lines, "\n"), "seen while running") {
		t.Errorf("snapshot not preferred: %q", lines)
	}
}

// Once the review is no longer running, the popup shows the last attempt's
// saved transcript instead of claiming none is stored.
func TestReviewDetail_ShowsTheSavedTranscriptAfterTheReview(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "progress.log")
	if err := saveReviewTranscript(reviewAttemptDir(logPath, "job-4"), "Please run /login · API Error: 403\nResume this session with:"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reviewAttemptDir(logPath, "job-4"), reviewEventsFile), []byte("10:48:01.000 POST /x/renew → 409 in 12ms\n"), 0600); err != nil {
		t.Fatal(err)
	}
	saveReviewDiagnostics(reviewAttemptDir(logPath, "job-4"), reviewAttemptDiagnostics{Attempt: "att-9", Provider: "claude", Started: time.Now().Add(-14 * time.Minute),
		Finished: time.Now().Add(-time.Minute), Outcome: "failed", Failure: &reviewExecutionDiagnostic{Category: "lease_rejected", Stage: "provider"}})
	tm, _, _ := liveFixture(t, "job-other") // a tmux server where job-4 is not running
	client := NewClient(reviewDetailServer(t, false).URL, "token")
	d := newReviewDetail(func() tea.Msg { return loadReviewDetail(context.Background(), client, 66, "job-4") }).
		withLive(func(n int) tea.Msg { return loadReviewLive(tm.run, logPath, "job-4", n) })
	d, _ = d.update(tea.WindowSizeMsg{Width: 90, Height: 30})
	d, _ = d.update(d.load())
	d, _ = d.update(d.liveCmd()())
	view := ansi.Strip(d.render())
	for _, want := range []string{"Diagnostics · newest attempt on this machine", "Failure: lease_rejected at provider", "Last attempt · saved ", "Runner events", "POST /x/renew → 409 in 12ms", "Harness output of the last attempt", "API Error: 403"} {
		if !strings.Contains(view, want) {
			t.Errorf("popup lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "not stored") || strings.Contains(view, "Live ·") {
		t.Errorf("a saved transcript must replace the not-stored note and is not live:\n%s", view)
	}
	checkHistoryFits(t, d.render(), 90, 30)
}

// Every review API call is traced with its status and latency, including
// failures; no tracing without a hook (#5602).
func TestReviewRequest_TracesStatusAndLatency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/renew") {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	c := NewClient(server.URL, "secret-token")
	if err := c.reviewRequest(context.Background(), "GET", "/projects/1/pr-review-repositories", nil, &map[string]any{}); err != nil {
		t.Fatalf("untraced call failed: %v", err)
	}
	type call struct {
		method, path string
		status       int
		err          error
	}
	var calls []call
	c.reviewTrace = func(method, path string, status int, took time.Duration, err error) {
		if took < 0 {
			t.Errorf("negative latency")
		}
		calls = append(calls, call{method, path, status, err})
	}
	_ = c.reviewRequest(context.Background(), "GET", "/projects/1/pr-review-repositories?limit=5", nil, &map[string]any{})
	_ = c.reviewRequest(context.Background(), "POST", "/projects/1/pr-review-runners/r/jobs/j/attempts/a/renew", map[string]int{"x": 1}, nil)
	if len(calls) != 2 || calls[0].status != 200 || calls[0].err != nil || calls[1].method != "POST" || calls[1].status != 409 || calls[1].err == nil {
		t.Fatalf("traced calls = %+v", calls)
	}
}

func TestReviewAttemptProgress_EventLogRecordsStepsAndCalls(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "progress.log")
	dir := reviewAttemptDir(logPath, "job-4")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, reviewEventsFile), []byte("old attempt line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := newReviewAttemptProgress(nil, "", logPath, "review 1", "review #4", "claude", nil)
	r.jobID, r.attempt = "job-4", "att-2"
	r.claim = "POST /projects/1/pr-review-runners/{runner}/jobs/{job}/claim → 200 in 12ms (claim at 10:48:00.000)"
	r.start()
	r.setStage("checkout")
	r.requestEvent("POST", "/projects/1/pr-review-runners/r/jobs/job-4/attempts/att-2/renew?x=secret", 200, 84*time.Millisecond, nil)
	r.requestEvent("POST", "/projects/1/pr-review-runners/r/jobs/job-4/attempts/att-2/result", 0, 30*time.Second, errReviewConnection)
	r.finish("failed - review failed at provider")
	events := strings.Join(readReviewEvents(dir, 100), "\n")
	for _, want := range []string{
		"attempt att-2 started with claude",
		"/jobs/{job}/claim → 200 in 12ms (claim at 10:48:00.000)",
		"stage checkout: checking out the PR",
		"POST /projects/1/pr-review-runners/{runner}/jobs/{job}/attempts/{attempt}/renew → 200 in 84ms",
		"/result → no response: ",
		"finished: failed - review failed at provider",
	} {
		if !strings.Contains(events, want) {
			t.Errorf("event log lacks %q:\n%s", want, events)
		}
	}
	if strings.Contains(events, "old attempt line") || strings.Contains(events, "secret") {
		t.Errorf("a new attempt must start a fresh log without query strings:\n%s", events)
	}
	if info, err := os.Stat(filepath.Join(dir, reviewEventsFile)); err != nil || info.Mode().Perm() != 0600 {
		t.Errorf("event log mode: %v %v", info, err)
	}
}

func TestReviewDiagnosticsLines_RunningAndFailed(t *testing.T) {
	start := time.Date(2026, 11, 27, 10, 48, 0, 0, time.Local)
	d := reviewAttemptDiagnostics{
		Attempt: "att-2", Provider: "claude", Version: "2.1.330 (Claude Code)", Binary: "/usr/local/bin/claude", Started: start,
		Stages: []reviewStageTime{
			{Name: "brief", Label: "preparing the brief", Start: start},
			{Name: "checkout", Label: "checking out the PR", Start: start.Add(2 * time.Second)},
			{Name: "child_guard", Label: "claude working", Start: start.Add(10 * time.Second)},
		},
		Deadline: start.Add(30 * time.Minute), Renewed: start.Add(90 * time.Second), LeaseExpires: start.Add(195 * time.Second),
	}
	running := strings.Join(reviewDiagnosticsLines(d, start.Add(3*time.Minute)), "\n")
	for _, want := range []string{
		"Harness: claude 2.1.330 (Claude Code) · /usr/local/bin/claude",
		"Attempt: att-2 · started 10:48:00 · 3m elapsed",
		"Stage: claude working for 2m",
		"Stages: preparing the brief 2s · checking out the PR 8s · claude working 2m",
		"Lease: expires 10:51:15 (15s left) · renewed 10:49:30 · deadline 11:18 (27m left)",
	} {
		if !strings.Contains(running, want) {
			t.Errorf("running diagnostics lack %q:\n%s", want, running)
		}
	}
	exit := 0
	d.ExitCode = &exit
	d.Finished, d.Outcome = start.Add(13*time.Minute), "failed - review failed at provider"
	d.Failure = &reviewExecutionDiagnostic{Category: "lease_rejected", Stage: "provider", ExitCode: &exit, StdoutBytes: 1200}
	failed := strings.Join(reviewDiagnosticsLines(d, start.Add(20*time.Minute)), "\n")
	for _, want := range []string{
		"finished after 13m: failed - review failed at provider",
		"claude working 12m",
		"Failure: lease_rejected at provider · exit 0 · harness output 1200 B stdout, 0 B stderr",
		"Lease: expires 10:51:15 (expired) · renewed 10:49:30 · deadline 11:18 (10m left)",
		"Harness exit: code 0",
	} {
		if !strings.Contains(failed, want) {
			t.Errorf("failed diagnostics lack %q:\n%s", want, failed)
		}
	}
	if strings.Contains(failed, "Stage: ") {
		t.Errorf("a finished attempt has no current stage:\n%s", failed)
	}
}

// The reporter keeps diagnostics.json current through the attempt, probing
// the harness version in a throwaway directory.
func TestReviewAttemptProgress_WritesDiagnostics(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fakeharness")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\npwd > marker-cwd\necho '9.9.9 (Fake Harness)'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "progress.log")
	deadline := time.Now().Add(29 * time.Minute)
	r := newReviewAttemptProgress(nil, "", logPath, "review 1", "review #4", "fakeharness", func() time.Time { return deadline })
	r.jobID, r.attempt, r.binary = "job-4", "att-2", bin
	firstExpiry := time.Now().Add(time.Minute).Truncate(time.Second)
	renewedExpiry := firstExpiry.Add(time.Minute)
	r.leaseExpires(firstExpiry)
	r.start()
	if d, _ := readReviewDiagnostics(reviewAttemptDir(logPath, "job-4")); !d.LeaseExpires.Equal(firstExpiry) {
		t.Errorf("claim lease expiry = %v, want %v", d.LeaseExpires, firstExpiry)
	}
	r.setStage("checkout")
	r.leaseRenewed(time.Now(), renewedExpiry)
	r.harnessExit(func() *int { c := 3; return &c }(), 0)
	exit := 3
	r.setFailure(reviewExecutionDiagnostic{Category: "provider_failed", Stage: "provider", ExitCode: &exit})
	dir := reviewAttemptDir(logPath, "job-4")
	for i := 0; i < 50; i++ { // the version probe runs alongside the attempt
		if d, _ := readReviewDiagnostics(dir); d.Version != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.finish("failed - review failed at provider")
	d, ok := readReviewDiagnostics(dir)
	if !ok || d.Attempt != "att-2" || d.Version != "9.9.9 (Fake Harness)" || d.Binary != bin || len(d.Stages) != 2 ||
		d.Stages[1].Name != "checkout" || d.Renewed.IsZero() || d.Finished.IsZero() || d.Failure == nil || *d.Failure.ExitCode != 3 ||
		!d.LeaseExpires.Equal(renewedExpiry) || d.ExitCode == nil || *d.ExitCode != 3 ||
		!d.Deadline.Equal(deadline.Round(0)) && d.Deadline.Sub(deadline).Abs() > time.Second {
		t.Fatalf("diagnostics = %+v (ok %v)", d, ok)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(logPath), "marker-cwd")); err == nil {
		t.Error("the version probe ran in the runner's directory")
	}
	if info, err := os.Stat(filepath.Join(dir, reviewDiagnosticsFile)); err != nil || info.Mode().Perm() != 0600 {
		t.Errorf("diagnostics mode: %v %v", info, err)
	}
}

func TestReviewPathTemplate(t *testing.T) {
	for path, want := range map[string]string{
		"/projects/1/pr-review-runners/2a2d-9/jobs/138c/attempts/question-7f/renew": "/projects/1/pr-review-runners/{runner}/jobs/{job}/attempts/{attempt}/renew",
		"/projects/1/pr-review-runners/2a2d-9/jobs/138c/claim":                      "/projects/1/pr-review-runners/{runner}/jobs/{job}/claim",
		"/projects/1/pr-review-runners/2a2d-9/heartbeat":                            "/projects/1/pr-review-runners/{runner}/heartbeat",
		"/projects/1/pr-review-runners":                                             "/projects/1/pr-review-runners",
		"/projects/1/pr-review-repositories":                                        "/projects/1/pr-review-repositories",
	} {
		if got := reviewPathTemplate(path); got != want {
			t.Errorf("reviewPathTemplate(%q) = %q, want %q", path, got, want)
		}
	}
	if got := reviewRequestEventLine("POST", "/projects/1/pr-review-runners/r1/jobs/j1/claim?x=1", 409, 30*time.Millisecond, nil); got != "POST /projects/1/pr-review-runners/{runner}/jobs/{job}/claim → 409 in 30ms" {
		t.Errorf("event line = %q", got)
	}
}

// The transcript on DISK is already sanitized, not only what the popup reads
// back (#7699): a `cat` or `less` of the file must not replay terminal
// escapes (screen clear, OSC title, OSC 52 clipboard, C1 CSI, bidi).
func TestReviewTranscript_FileOnDiskHoldsNoControlBytes(t *testing.T) {
	dir := reviewAttemptDir(filepath.Join(t.TempDir(), "progress.log"), "job-4")
	hostile := "\x1b[2J\x1b[Hcleared\x1b]0;pwned\x07 \x1b]52;c;ZXZpbA==\x07 \x9b31mc1\u202edlrow\x00end\nnext line\x1b[0m\n"
	if err := saveReviewTranscript(dir, hostile); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, reviewTranscriptFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range string(raw) {
		if (unicode.IsControl(r) && r != '\n') || unicode.Is(unicode.Cf, r) {
			t.Fatalf("raw transcript file holds control/format rune %U: %q", r, raw)
		}
	}
	if !strings.Contains(string(raw), "cleared") || !strings.Contains(string(raw), "next line") {
		t.Errorf("readable text lost: %q", raw)
	}
}
