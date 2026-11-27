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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// liveFixture starts an isolated tmux server with one "slot pane" printing
// harness output, tagged as running job, plus a console progress log.
func liveFixture(t *testing.T, job string) (*TmuxManager, string, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-live-" + itoa(os.Getpid()))
	_, _ = tm.run("kill-server")
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	out, err := tm.run("new-session", "-d", "-s", "vera", "-x", "120", "-y", "40", "-P", "-F", "#{pane_id}",
		"for i in $(seq 1 60); do echo \"claude step $i\"; done; printf '\\033[31mred\\033[0m done\\n'; sleep 300")
	if err != nil {
		t.Skipf("cannot start tmux: %v", err)
	}
	pane := strings.TrimSpace(out)
	if _, err := tm.run("set-option", "-p", "-t", pane, reviewLiveJobOption, job); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "progress.log")
	log := "10:48 review 1 · Claimed PR #4\n10:48 review 1 · review #4 · claude working · 1m elapsed\n10:48 answer 1 · answer #41 · claude working\n10:49 review 1 · review #5 · checking out the PR\n"
	if err := os.WriteFile(logPath, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ { // wait for the output to land in the pane
		if got, _ := tm.run("capture-pane", "-p", "-t", pane); strings.Contains(got, "done") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return tm, pane, logPath
}

func TestLoadReviewLive_CapturesTheTaggedSlotPane(t *testing.T) {
	tm, _, logPath := liveFixture(t, "job-4")

	live := loadReviewLive(tm.run, logPath, "job-4", 4)
	if !live.running {
		t.Fatal("tagged pane not found")
	}
	output := strings.Join(live.output, "\n")
	if !strings.Contains(output, "claude step 1") || !strings.Contains(output, "claude step 60") || !strings.Contains(output, "red done") {
		t.Errorf("captured output incomplete (scrollback included?):\n%s", output)
	}
	if strings.Contains(output, "\x1b") {
		t.Errorf("escape sequences reached the popup: %q", output)
	}
	if got := strings.Join(live.progress, "\n"); !strings.Contains(got, "Claimed PR #4") || !strings.Contains(got, "review #4 · claude working") ||
		strings.Contains(got, "#41") || strings.Contains(got, "#5 ") {
		t.Errorf("progress must be this PR's lines only:\n%s", got)
	}

	if other := loadReviewLive(tm.run, logPath, "job-9", 9); other.running || len(other.output) != 0 {
		t.Error("a review not running here must not show live output")
	}
	if bad := loadReviewLive(tm.run, logPath, "job 4;x", 4); bad.running {
		t.Error("an invalid job id must not match a pane")
	}
}

// The popup of a running review splits into the result and a live section
// that follows the newest output, scrolls on its own, and is focused by Tab
// or a click; a review not running here keeps the recorded-result view.
func TestReviewDetail_LiveSectionScrollsAndFocusesOnItsOwn(t *testing.T) {
	tm, _, logPath := liveFixture(t, "job-4")
	client := NewClient(reviewDetailServer(t, false).URL, "token")
	d := newReviewDetail(func() tea.Msg { return loadReviewDetail(context.Background(), client, 66, "job-4") }).
		withLive(func(n int) tea.Msg { return loadReviewLive(tm.run, logPath, "job-4", n) })
	d, _ = d.update(tea.WindowSizeMsg{Width: 90, Height: 30})
	d, _ = d.update(d.load())
	liveMsg := d.liveCmd()()
	d, _ = d.update(liveMsg)

	view := ansi.Strip(d.render())
	for _, want := range []string{"PR #4 · Add late payment fee calculation", "Live · following", "red done", "Tab or click: switch section"} {
		if !strings.Contains(view, want) {
			t.Fatalf("live popup lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "not stored") {
		t.Errorf("a running review must not claim no transcript exists:\n%s", view)
	}
	checkHistoryFits(t, d.render(), 90, 30)

	// The wheel over the result scrolls only the result.
	liveBefore := d.liveOffset
	d, _ = d.update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, Y: 1})
	if d.offset == 0 || d.liveOffset != liveBefore {
		t.Errorf("wheel over the result: offset %d, live offset %d (was %d)", d.offset, d.liveOffset, liveBefore)
	}
	// A click in the live section focuses it; scrolling up there pauses following.
	d, _ = d.update(tea.MouseClickMsg{Button: tea.MouseLeft, Y: d.resultPage() + 2})
	if d.focus != 1 {
		t.Fatalf("click in the live section did not focus it (focus %d)", d.focus)
	}
	d, _ = d.update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if d.liveFollow || !strings.Contains(ansi.Strip(d.render()), "paused · End follows") {
		t.Errorf("scrolling up must pause following:\n%s", ansi.Strip(d.render()))
	}
	// A refresh keeps the paused position; End resumes following.
	paused := d.liveOffset
	d, _ = d.update(d.liveCmd()())
	if d.liveOffset != paused {
		t.Errorf("refresh moved a paused view from %d to %d", paused, d.liveOffset)
	}
	d, _ = d.update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !d.liveFollow {
		t.Error("End must resume following")
	}
	// Tab returns focus to the result.
	d, _ = d.update(tea.KeyPressMsg{Code: tea.KeyTab})
	if d.focus != 0 {
		t.Errorf("Tab did not switch focus back (focus %d)", d.focus)
	}

	// The live view refreshes itself, asking with the PR number once known.
	if d.followUp(liveMsg) == nil {
		t.Fatal("a live message must schedule the next refresh")
	}
	asked := 0
	d.live = func(n int) tea.Msg { asked = n; return reviewLiveMsg{} }
	if cmd := d.followUp(reviewLiveTickMsg{}); cmd == nil || cmd() == nil || asked != 4 {
		t.Errorf("tick must reload the live view for PR #4 (asked %d)", asked)
	}

	// The review ends: the popup returns to the recorded-result view.
	d, _ = d.update(reviewLiveMsg{})
	if view := ansi.Strip(d.render()); strings.Contains(view, "Live ·") || !strings.Contains(view, "not stored") || d.focus != 0 {
		t.Errorf("finished review still shows a live section:\n%s", view)
	}
}

// The runner tags its slot pane with the job while the attempt runs, so the
// popup can find it, and removes the tag when the attempt ends.
func TestReviewAttemptProgress_TagsThePaneWithTheJob(t *testing.T) {
	tm, pane, _ := liveFixture(t, "")
	_, _ = tm.run("set-option", "-p", "-u", "-t", pane, reviewLiveJobOption)
	r := newReviewAttemptProgress(tm.run, pane, "", "review 1", "review #4", "claude", nil)
	r.jobID = "job-4"
	r.start()
	if got := reviewLivePane(tm.run, "job-4"); got != pane {
		t.Errorf("running attempt: pane for job-4 = %q, want %q", got, pane)
	}
	r.finish("clean.")
	if got := reviewLivePane(tm.run, "job-4"); got != "" {
		t.Errorf("finished attempt still tagged on %q", got)
	}
}
