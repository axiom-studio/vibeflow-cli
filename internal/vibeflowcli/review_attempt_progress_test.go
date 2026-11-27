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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every slot of a console and its history pane must agree on one log file.
func TestReviewProgressLogPath_SlotsAndHistoryShareOneLog(t *testing.T) {
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	console := reviewWatchOptions{ProjectID: 13, RepositoryLinkID: 3, GitProvider: "github", Kind: "local", Name: "mac.local"}
	want := reviewProgressLogPath("https://cloud.example", console)
	for _, slot := range reviewSlotList(1, 1) {
		if got := reviewProgressLogPath("https://cloud.example", reviewSlotOptions(console, slot)); got != want {
			t.Errorf("slot %s logs to %s, history reads %s", slot.Label, got, want)
		}
	}
	other := console
	other.RepositoryLinkID = 4
	if reviewProgressLogPath("https://cloud.example", other) == want {
		t.Error("another repository binding must not share the log")
	}
}

// The border line and the log follow the attempt on a real tmux pane, and
// finish removes the border again.
func TestReviewAttemptProgress_BorderAndLogFollowTheAttempt(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-progress-" + itoa(os.Getpid()))
	_, _ = tm.run("kill-server")
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	out, err := tm.run("new-session", "-d", "-s", "vera", "-P", "-F", "#{pane_id}", "sleep 300")
	if err != nil {
		t.Skipf("cannot start tmux: %v", err)
	}
	pane := strings.TrimSpace(out)
	logPath := filepath.Join(t.TempDir(), "progress.log")
	deadline := time.Now().Add(29 * time.Minute)
	r := newReviewAttemptProgress(tm.run, pane, logPath, "review 1", "review #49", "claude", func() time.Time { return deadline })

	r.start()
	r.setStage("child_guard")
	r.leaseRenewed(time.Date(2026, 11, 27, 8, 1, 0, 0, time.Local))
	r.refresh(false)
	border, _ := tm.run("display-message", "-p", "-t", pane, "#{pane-border-status}|#{"+reviewProgressOption+"}")
	if !strings.HasPrefix(border, "top|Vera · review #49 · claude working · 0s elapsed · 2") ||
		!strings.Contains(border, "m left · lease renewed 08:01") {
		t.Errorf("border = %q", strings.TrimSpace(border))
	}

	r.finish("failed - review failed at provider")
	after, _ := tm.run("display-message", "-p", "-t", pane, "#{pane-border-status}|#{"+reviewProgressOption+"}")
	if strings.TrimSpace(after) != "off|" {
		t.Errorf("finish must remove the border line, got %q", strings.TrimSpace(after))
	}
	log := strings.Join(readReviewProgressLog(logPath, 10), "\n")
	for _, want := range []string{"review 1 · review #49 · preparing the brief", "review 1 · review #49 · claude working", "review 1 · review #49 · failed - review failed at provider after 0s"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

// Outside tmux (no pane) the log still works and nothing is sent to tmux.
func TestReviewAttemptProgress_WithoutTmuxOnlyLogs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "sub", "progress.log")
	r := newReviewAttemptProgress(nil, "", logPath, "answer 1", "answer #7", "codex", nil)
	r.start()
	r.finish("answered with 2 citations.")
	if lines := readReviewProgressLog(logPath, 10); len(lines) != 2 || !strings.Contains(lines[1], "answer #7 · answered with 2 citations.") {
		t.Errorf("log = %q", lines)
	}
	var none *reviewAttemptProgress // A runner outside a console slot has no reporter.
	none.start()
	none.setStage("checkout")
	none.leaseRenewed(time.Now())
	none.appendLog("ignored")
	none.finish("x")
}

func TestReviewAttemptProgress_JobLabelIsPrintableOnly(t *testing.T) {
	r := newReviewAttemptProgress(nil, "", "", "review 1", "review \x1b]0;pwned\x07abc", "claude", nil)
	if strings.ContainsAny(r.line(time.Now()), "\x1b\x07") {
		t.Errorf("control bytes reached the progress line: %q", r.line(time.Now()))
	}
}

func TestTrimReviewProgressLog_KeepsTheNewestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.log")
	r := &reviewAttemptProgress{logPath: path}
	line := strings.Repeat("x", 400)
	for i := 0; i < 400; i++ { // ~160 KiB, well past the cap
		r.appendLog(fmt.Sprintf("%03d %s", i, line))
	}
	// Trimmed to the newest reviewProgressLogKeep lines whenever it passes
	// reviewProgressLogMax, so it can never grow past either bound for long.
	info, err := os.Stat(path)
	if err != nil || info.Size() > int64(max(reviewProgressLogMax, (reviewProgressLogKeep+1)*(len(line)+5))) {
		t.Fatalf("log not bounded: %v %v", info.Size(), err)
	}
	lines := readReviewProgressLog(path, 1000)
	if len(lines) > reviewProgressLogKeep+1 || !strings.HasPrefix(lines[len(lines)-1], "399 ") {
		t.Errorf("kept %d lines, last %q", len(lines), lines[len(lines)-1][:8])
	}
}

func TestRoundElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0s", 59 * time.Second: "59s", 90 * time.Second: "1m", 29 * time.Minute: "29m", 75 * time.Minute: "1h15m"} {
		if got := roundElapsed(d); got != want {
			t.Errorf("roundElapsed(%v) = %q, want %q", d, got, want)
		}
	}
}
