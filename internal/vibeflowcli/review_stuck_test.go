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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

// stuckLoginScreen is a harness script that shows PR #49's error and idles;
// with logOut it also logs the harness out first, so its status check agrees.
func stuckLoginScreen(logOut bool) func(*reviewExecution, func(string) string) string {
	return func(_ *reviewExecution, mark func(string) string) string {
		pre := ""
		if logOut {
			pre = "touch " + shellQuote(mark("logged-out")) + "\n"
		}
		return pre + "printf '⏺ Please run /login · API Error: 403 Built-in models are available only\\n  through HelloSeal.ai\\n❯ Once fixed, resend your message\\n'\nexec sleep 300\n"
	}
}

// waitPane waits longer than veraPane.waitFor: these tests sit out the real
// stuck threshold inside the built binary.
func waitPane(t *testing.T, v *veraPane, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(reviewStuckAfter + 30*time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("missing %s after %s:\n%s", what, reviewStuckAfter+30*time.Second, v.capture())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// End to end in a real pane (#7691 + #7697): the harness shows PR #49's login
// error but its own status check says it is logged in, so the text may be
// prompt-injected review output. The attempt fails with the real category
// and Vera keeps listening instead of stopping. Takes ~50 s; -short skips.
func TestReviewWatchInteractiveStuckScreenUnconfirmedKeepsListening(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the real stuck threshold")
	}
	v := startVeraPane(t, true, stuckLoginScreen(false))
	v.waitFor("harness error screen", v.shows("Please run /login"))
	waitPane(t, v, "the unconfirmed-error notice", v.shows("its status check does not confirm it; this attempt failed and Vera keeps listening"))
	if v.dead() || strings.Contains(v.capture(), "Vera stopped") {
		t.Fatalf("an unconfirmed error screen stopped the runner:\n%s", v.capture())
	}
	if p := v.lastReceipt(); !strings.Contains(p.Failure, "authentication_required") || strings.Contains(p.Failure, "lease_rejected") {
		t.Fatalf("stuck attempt recorded as %q", p.Failure)
	}
}

// End to end in a real pane: the harness really logged out, its status check
// confirms it, and Vera stops with the login hint without re-claiming.
// Takes ~50 s; -short skips.
func TestReviewWatchInteractiveStuckScreenConfirmedLogoutStopsVera(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the real stuck threshold")
	}
	v := startVeraPane(t, true, stuckLoginScreen(true))
	v.waitFor("harness error screen", v.shows("Please run /login"))
	waitPane(t, v, "Vera to stop", v.dead)
	text := v.capture()
	for _, want := range []string{"Vera stopped: the claude harness is not logged in on this machine (it sat on an error screen for 45 s), as its status check confirms", "run claude and use /login", "The review stays queued"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pane missing %q:\n%s", want, text)
		}
	}
	if p := v.lastReceipt(); !strings.Contains(p.Failure, "authentication_required") {
		t.Fatalf("stuck attempt recorded as %q", p.Failure)
	}
	if v.claims[7].Load() != 1 {
		t.Fatalf("Vera re-claimed after a confirmed logout (claims=%d)", v.claims[7].Load())
	}
}

// The runner stops on a harness-output error only when the harness's own
// status check confirms the logout, or after the same failure on
// reviewSuspectLimit different reviews; one review repeating it (a
// prompt-injected PR re-claimed) never stops it (#7697).
func TestHarnessOutputProblem_StopsOnlyWhenConfirmedOrRepeatedAcrossReviews(t *testing.T) {
	statusFile := filepath.Join(t.TempDir(), "status")
	harness := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n[ \"$1 $2\" = 'auth status' ] || exit 0\nif [ -f " + shellQuote(statusFile) + " ]; then echo '{\"loggedIn\": false}'; exit 1; fi\necho '{\"loggedIn\": true}'\n"
	if err := os.WriteFile(harness, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Providers["claude"] = Provider{Binary: harness}
	var out bytes.Buffer
	w := &reviewWatch{cfg: cfg, options: reviewWatchOptions{Provider: "claude"}, output: &out}
	ctx := context.Background()

	// The same review repeating the error (re-claimed) never stops the runner.
	for i := 0; i < 5; i++ {
		w.harnessOutputProblem(ctx, "job-evil", "authentication_required", "")
	}
	if w.harnessFatal != nil || !strings.Contains(out.String(), "its status check does not confirm it") {
		t.Fatalf("one review stopped the runner: %v\n%s", w.harnessFatal, out.String())
	}
	// Different reviews failing the same way stop it at the limit.
	w.harnessOutputProblem(ctx, "job-2", "access_denied", "")
	if w.harnessFatal != nil {
		t.Fatalf("stopped after 2 reviews: %v", w.harnessFatal)
	}
	w.harnessOutputProblem(ctx, "job-3", "access_denied", "")
	if w.harnessFatal == nil || !strings.Contains(w.harnessFatal.Error(), "on 3 different reviews in a row") {
		t.Fatalf("not stopped after 3 different reviews: %v", w.harnessFatal)
	}

	// A logout confirmed by the status check stops the runner at once.
	w.harnessFatal, w.suspectJobs = nil, nil
	if err := os.WriteFile(statusFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	w.harnessOutputProblem(ctx, "job-4", "authentication_required", " (it sat on an error screen for 45 s)")
	if w.harnessFatal == nil || !strings.Contains(w.harnessFatal.Error(), "is not logged in on this machine (it sat on an error screen for 45 s), as its status check confirms") {
		t.Fatalf("confirmed logout did not stop the runner: %v", w.harnessFatal)
	}

	// A harness with no status command can only be stopped by the repeat rule.
	w = &reviewWatch{cfg: cfg, options: reviewWatchOptions{Provider: "gemini"}, output: &out}
	w.harnessOutputProblem(ctx, "job-1", "authentication_required", "")
	if w.harnessFatal != nil {
		t.Fatalf("an unconfirmable harness stopped on one review: %v", w.harnessFatal)
	}
}

// "In a row" (#7700): a review that succeeds clears the suspicion, so three
// suspect reviews separated by a working one never stop the runner — a PR
// author cannot stop a runner by spreading injected errors across PRs.
func TestHarnessOutputProblem_SuccessfulReviewResetsTheCount(t *testing.T) {
	source, execution := reviewTestRepo(t)
	content := json.RawMessage(`{"findings":[]}`)
	digest := sha256.Sum256(content)
	brief := reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: content}
	response := reviewResultEnvelope(t, map[string]any{
		"schema_version": 1, "brief_digest": brief.Digest,
		"head_sha": execution.Review.HeadSHA, "base_sha": execution.Review.BaseSHA,
		"outcome": "clean", "summary": "fixture", "new_findings": []any{}, "reconciliations": []any{},
	})
	root := t.TempDir()
	provider := filepath.Join(root, "provider")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\n"+reviewWriteResult(response)), 0700); err != nil {
		t.Fatal(err)
	}
	var submissions atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/brief"):
			json.NewEncoder(w).Encode(brief)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
			submissions.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/renew"):
			json.NewEncoder(w).Encode(execution)
		case r.Method == "POST":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.Providers["claude"] = Provider{Binary: provider}
	cfg.Providers["gemini"] = Provider{Binary: provider} // no status command: only the repeat rule can stop it
	var out bytes.Buffer
	receipt := &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution}
	w := &reviewWatch{
		client: NewClient(server.URL, "token"), cfg: cfg, root: root, output: &out,
		state:   reviewRunnerState{ID: execution.Attempt.RunnerID, Pending: receipt},
		options: reviewWatchOptions{ProjectID: 1, Repository: source, Provider: "claude", Timeout: 30 * time.Second},
	}
	ctx := context.Background()
	w.options.Provider = "gemini"
	w.harnessOutputProblem(ctx, "job-a", "authentication_required", "")
	w.harnessOutputProblem(ctx, "job-b", "access_denied", "")
	w.options.Provider = "claude"
	if w.harnessFatal != nil {
		t.Fatalf("stopped after 2 reviews: %v", w.harnessFatal)
	}

	if err := w.advance(ctx, true); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if submissions.Load() != 1 || len(receipt.Result) == 0 || receipt.Failure != "" {
		t.Fatalf("the review did not succeed: submissions=%d failure=%q\n%s", submissions.Load(), receipt.Failure, out.String())
	}

	w.options.Provider = "gemini"
	w.harnessOutputProblem(ctx, "job-c", "authentication_required", "")
	if w.harnessFatal != nil {
		t.Fatalf("suspect reviews split by a working one stopped the runner: %v", w.harnessFatal)
	}
	w.harnessOutputProblem(ctx, "job-d", "authentication_required", "")
	w.harnessOutputProblem(ctx, "job-e", "authentication_required", "")
	if w.harnessFatal == nil {
		t.Fatal("three suspect reviews in a row after the reset did not stop the runner")
	}
}
