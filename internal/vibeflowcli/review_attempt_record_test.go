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
	tm, _, _ := liveFixture(t, "job-other") // a tmux server where job-4 is not running
	client := NewClient(reviewDetailServer(t, false).URL, "token")
	d := newReviewDetail(func() tea.Msg { return loadReviewDetail(context.Background(), client, 66, "job-4") }).
		withLive(func(n int) tea.Msg { return loadReviewLive(tm.run, logPath, "job-4", n) })
	d, _ = d.update(tea.WindowSizeMsg{Width: 90, Height: 30})
	d, _ = d.update(d.load())
	d, _ = d.update(d.liveCmd()())
	view := ansi.Strip(d.render())
	for _, want := range []string{"Last attempt · saved ", "Runner events", "POST /x/renew → 409 in 12ms", "Harness output of the last attempt", "API Error: 403"} {
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
	r.start()
	r.setStage("checkout")
	r.requestEvent("POST", "/projects/1/pr-review-runners/r/jobs/job-4/attempts/att-2/renew?x=secret", 200, 84*time.Millisecond, nil)
	r.requestEvent("POST", "/projects/1/pr-review-runners/r/jobs/job-4/attempts/att-2/result", 0, 30*time.Second, errReviewConnection)
	r.finish("failed - review failed at provider")
	events := strings.Join(readReviewEvents(dir, 100), "\n")
	for _, want := range []string{
		"attempt att-2 started with claude",
		"stage checkout: checking out the PR",
		"POST /projects/1/pr-review-runners/r/jobs/job-4/attempts/att-2/renew → 200 in 84ms",
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
