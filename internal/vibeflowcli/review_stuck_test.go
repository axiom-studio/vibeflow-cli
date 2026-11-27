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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The screen PR #49's harness sat on for 13 minutes (#7691).
const pr49StuckScreen = ` ▐▛███▛█   Claude Code v2.1.330
❯ Your complete PR review task is in the file /x/task.txt.
⏺ Please run /login · API Error: 403 Built-in models are available only
  through HelloSeal.ai
────────────────────────────────────────────────
❯ Once fixed, resend your message
`

func TestClassifyStuckHarnessScreen(t *testing.T) {
	for _, tc := range []struct{ name, screen, want string }{
		{"PR #49 login screen", pr49StuckScreen, "authentication_required"},
		{"claude 403 without login hint", "⏺ API Error: 403 Forbidden\n❯ ", "access_denied"},
		{"claude 401", "⏺ API Error: 401 {\"type\":\"error\"}\n", "authentication_required"},
		{"codex 401", "■ unexpected status 401 Unauthorized: Missing bearer\n", "authentication_required"},
		{"codex 403", "■ unexpected status 403 Forbidden\n", "access_denied"},
		// A review's screen shows code and diffs; bare status codes prove nothing.
		{"diff with 403", "  -	return http.StatusForbidden // 403\n  +	return 401\n⏺ Reviewing auth.go\n", ""},
		{"longer code", "⏺ API Error: 4031 is not a status\n", ""},
		{"progress", "✻ Thinking… (45s · esc to interrupt)\n", ""},
		{"error scrolled far up", "⏺ API Error: 403\n" + strings.Repeat("⏺ reading file\n", reviewStuckLines+1), ""},
	} {
		if got := classifyStuckHarnessScreen(tc.screen); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The watcher fires only for a screen that stays still on a harness error:
// not while the screen changes, and not for a still screen without one.
func TestWatchStuckHarness_FiresOnlyForAStillErrorScreen(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-stuck-" + itoa(os.Getpid()))
	_, _ = tm.run("kill-server")
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	start := func(name, command string) string {
		out, err := tm.run("new-session", "-d", "-s", name, "-x", "100", "-y", "20", "-P", "-F", "#{pane_id}", command)
		if err != nil {
			t.Skipf("cannot start tmux: %v", err)
		}
		return strings.TrimSpace(out)
	}
	stuckPane := start("stuck", "printf '⏺ Please run /login · API Error: 403 Built-in models\\n❯ Once fixed, resend your message\\n'; sleep 300")
	busyPane := start("busy", "i=0; while :; do i=$((i+1)); echo \"⏺ API Error: 403 seen in a diff, line $i\"; sleep 0.05; done")
	quietPane := start("quiet", "echo '✻ Thinking…'; sleep 300")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stuck, busy, quiet atomic.Value
	watchStuckHarness(ctx, tm.run, stuckPane, 30*time.Millisecond, 300*time.Millisecond, func(c string) { stuck.Store(c) })
	watchStuckHarness(ctx, tm.run, busyPane, 30*time.Millisecond, 300*time.Millisecond, func(c string) { busy.Store(c) })
	watchStuckHarness(ctx, tm.run, quietPane, 30*time.Millisecond, 300*time.Millisecond, func(c string) { quiet.Store(c) })
	deadline := time.Now().Add(5 * time.Second)
	for stuck.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got, _ := stuck.Load().(string); got != "authentication_required" {
		t.Fatalf("still error screen: got %q", got)
	}
	time.Sleep(time.Second)
	if busy.Load() != nil || quiet.Load() != nil {
		t.Errorf("fired for a changing screen (%v) or a still screen without an error (%v)", busy.Load(), quiet.Load())
	}
	// A failed attempt carries the stuck category, not "cancelled".
	failed, stop := context.WithCancelCause(context.Background())
	stop(&reviewHarnessStuckError{category: "access_denied"})
	if got := reviewCancellationCategory(failed); got != "access_denied" {
		t.Errorf("cancellation category = %q", got)
	}
}

// End to end in a real pane: an interactive harness stuck on PR #49's login
// error fails its attempt as authentication_required within about a minute,
// instead of holding the review until the lease ends, and Vera stops with
// the login hint. Takes ~50 s (the real threshold), so -short skips it.
func TestReviewWatchInteractiveStuckOnLoginErrorFailsEarly(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the real stuck threshold")
	}
	v := startVeraPane(t, true, func(*reviewExecution, func(string) string) string {
		return "printf '⏺ Please run /login · API Error: 403 Built-in models are available only\\n  through HelloSeal.ai\\n❯ Once fixed, resend your message\\n'\nexec sleep 300\n"
	})
	v.waitFor("harness error screen", v.shows("Please run /login"))
	deadline := time.Now().Add(reviewStuckAfter + 30*time.Second)
	for !v.dead() {
		if time.Now().After(deadline) {
			t.Fatalf("Vera still waiting on a stuck harness after %s:\n%s", reviewStuckAfter+30*time.Second, v.capture())
		}
		time.Sleep(200 * time.Millisecond)
	}
	text := v.capture()
	for _, want := range []string{"Vera stopped: the claude harness is not logged in on this machine (it sat on an error screen for 45 s)", "run claude and use /login", "The review stays queued"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pane missing %q:\n%s", want, text)
		}
	}
	if p := v.lastReceipt(); !strings.Contains(p.Failure, "authentication_required") || strings.Contains(p.Failure, "lease_rejected") {
		t.Fatalf("stuck attempt recorded as %q", p.Failure)
	}
	if v.claims[7].Load() != 1 {
		t.Fatalf("Vera re-claimed after a stuck harness (claims=%d)", v.claims[7].Load())
	}
}
