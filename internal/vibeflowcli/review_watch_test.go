//go:build darwin || linux

package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// reviewWatchServer serves one claimable review per repository link. claims
// counts claims per link; results counts accepted results.
func reviewWatchServer(t *testing.T, executions map[int64]*reviewExecution) (server *httptest.Server, claims map[int64]*atomic.Int64, results *atomic.Int64) {
	t.Helper()
	claims, results = map[int64]*atomic.Int64{}, new(atomic.Int64)
	for link := range executions {
		claims[link] = new(atomic.Int64)
	}
	var mu sync.Mutex
	runners := map[string]int64{} // runner ID -> link
	digest := sha256.Sum256([]byte("{}"))
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		link := int64(0)
		for id, l := range runners {
			if strings.Contains(r.URL.Path, "/"+id) {
				link = l
			}
		}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pr-review-summaries"): // Vera's history pane.
			fmt.Fprint(w, `{"summaries":[]}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pr-review-repositories"):
			json.NewEncoder(w).Encode(map[string]any{"repositories": []any{}, "supported_runner_capabilities": []string{"repository_review_v1"}})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			id, _ := body["id"].(string)
			l, _ := body["repository_link_id"].(float64)
			runners[id] = int64(l)
			executions[int64(l)].Attempt.RunnerID = id
			body["user_id"] = 42
			json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/work"):
			reviews := []reviewJob{} // Offered until claimed, so a listener reviews it once.
			if claims[link].Load() == 0 {
				reviews = append(reviews, executions[link].Review)
			}
			json.NewEncoder(w).Encode(map[string]any{"reviews": reviews})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			claims[link].Add(1)
			json.NewEncoder(w).Encode(executions[link])
		case strings.HasSuffix(r.URL.Path, "/renew"):
			json.NewEncoder(w).Encode(executions[link])
		case strings.HasSuffix(r.URL.Path, "/brief"):
			json.NewEncoder(w).Encode(reviewBrief{RoundID: executions[link].Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: json.RawMessage(`{}`)})
		case strings.HasSuffix(r.URL.Path, "/result"):
			results.Add(1)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"), strings.HasSuffix(r.URL.Path, "/fail"), r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected review API: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return server, claims, results
}

// reviewWatchResult writes the harness result for e, with findings new findings.
func reviewWatchResult(t *testing.T, e *reviewExecution, findings int) string {
	t.Helper()
	digest := sha256.Sum256([]byte("{}"))
	var list []any
	for i := range findings {
		list = append(list, map[string]any{"key": string(rune('a' + i))})
	}
	outcome := "clean"
	if findings > 0 {
		outcome = "changes_requested"
	}
	path := filepath.Join(t.TempDir(), "result.json")
	result := map[string]any{"schema_version": 1, "brief_digest": hex.EncodeToString(digest[:]), "head_sha": e.Review.HeadSHA, "base_sha": e.Review.BaseSHA, "outcome": outcome, "summary": "done", "new_findings": list, "reconciliations": []any{}}
	if err := saveReviewJSON(path, map[string]any{"result": result, "failure_reason": nil}); err != nil {
		t.Fatal(err)
	}
	return path
}

func runReviewWatch(ctx context.Context, out *bytes.Buffer, args ...string) error {
	cmd := reviewWatchCmd()
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd.ExecuteContext(ctx)
}

// A foreground listener shows each review's lifecycle. Without a terminal
// the harness runs headless and its untrusted output is never relayed.
func TestReviewWatchForegroundLifecycle(t *testing.T) {
	withTempRoot(t)
	repo, e := reviewTestRepo(t)
	e.Review.Number = 42
	server, claims, results := reviewWatchServer(t, map[int64]*reviewExecution{7: e})
	provider := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\necho HARNESS-LIVE-LINE\necho HARNESS-ERR-LINE >&2\ncat " + shellQuote(reviewWatchResult(t, e, 2)) + " > ../../result.json\n"
	if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "stream-api-canary"
	cfg.Providers["claude"] = Provider{Binary: provider}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runReviewWatch(context.Background(), &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "stream-test", "--once"); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	text := out.String()
	t.Logf("listener output:\n%s", text)
	last := 0
	for _, want := range []string{reviewListeningLine, "Claimed PR #42 at " + e.Review.HeadSHA[:12] + "; starting a fresh Vera with claude.", "Review worktree ready: ", "Running claude headless", "Result: changes requested with 2 new findings.", "Review worktree removed.", "Result sent to VibeFlow", reviewListeningLine} {
		i := strings.Index(text[last:], want)
		if i < 0 {
			t.Fatalf("output missing %q after offset %d:\n%s", want, last, text)
		}
		last += i + len(want)
	}
	if strings.Contains(text, "HARNESS-") || strings.Contains(text, cfg.APIToken) {
		t.Fatalf("headless harness output or token relayed:\n%s", text)
	}
	if claims[7].Load() != 1 || results.Load() != 1 {
		t.Fatalf("claims=%d results=%d", claims[7].Load(), results.Load())
	}
	if work, _ := filepath.Glob(filepath.Join(RootDir(), "review-runners", "*", "work", "*")); len(work) != 0 {
		t.Fatalf("review worktree kept: %v", work)
	}
}

// Separate listeners (one per Vera session) share review_concurrency: at
// capacity a listener keeps listening and does not claim.
func TestReviewWatchListenersShareCapacity(t *testing.T) {
	withTempRoot(t)
	repo, first := reviewTestRepo(t)
	_, second := reviewTestRepo(t)
	second.Review.RepositoryLinkID = 8
	server, claims, results := reviewWatchServer(t, map[int64]*reviewExecution{7: first, 8: second})
	marks := t.TempDir()
	provider := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ntouch " + shellQuote(filepath.Join(marks, "started")) + "\nwhile [ ! -f " + shellQuote(filepath.Join(marks, "release")) + " ]; do sleep 0.05; done\ncat " + shellQuote(reviewWatchResult(t, first, 0)) + " > ../../result.json\n"
	if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken, cfg.ReviewConcurrency = server.URL, "capacity-canary", 1
	cfg.Providers["claude"] = Provider{Binary: provider}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	var firstOut, secondOut bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runReviewWatch(context.Background(), &firstOut, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "first", "--once")
	}()
	deadline := time.Now().Add(15 * time.Second)
	for _, err := os.Stat(filepath.Join(marks, "started")); err != nil; _, err = os.Stat(filepath.Join(marks, "started")) {
		if time.Now().After(deadline) {
			t.Fatal("first review never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := runReviewWatch(context.Background(), &secondOut, "--project", "1", "--repo", repo, "--repository-link", "8", "--provider", "claude", "--name", "second", "--once"); err != nil {
		t.Fatalf("%v\n%s", err, secondOut.String())
	}
	if claims[8].Load() != 0 || strings.Contains(secondOut.String(), "Claimed") || !strings.Contains(secondOut.String(), reviewListeningLine) {
		t.Fatalf("listener at capacity claimed (claims=%d):\n%s", claims[8].Load(), secondOut.String())
	}
	if err := os.WriteFile(filepath.Join(marks, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("%v\n%s", err, firstOut.String())
	}
	if claims[7].Load() != 1 || results.Load() != 1 {
		t.Fatalf("first review claims=%d results=%d", claims[7].Load(), results.Load())
	}
	// With the slot free again, the second listener claims its review.
	os.Remove(filepath.Join(marks, "release"))
	if err := os.WriteFile(filepath.Join(marks, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	secondOut.Reset()
	_ = runReviewWatch(context.Background(), &secondOut, "--project", "1", "--repo", repo, "--repository-link", "8", "--provider", "claude", "--name", "second", "--once")
	if claims[8].Load() != 1 {
		t.Fatalf("second listener never claimed after the slot freed:\n%s", secondOut.String())
	}
}

// veraPane is a real Vera tmux session whose listener runs the built CLI
// against reviewWatchServer, with a fake interactive harness.
type veraPane struct {
	t        *testing.T
	tm       *TmuxManager
	session  string
	e        *reviewExecution
	claims   map[int64]*atomic.Int64
	results  *atomic.Int64
	marks    string
	claudeDB string // The listener's private Claude config file.
	token    string
}

// startVeraPane starts Vera with a fake "claude" harness whose body is
// script(e, mark). The fake answers `auth status` as logged in, like the real
// CLI; loggedIn false makes it report the opposite.
func startVeraPane(t *testing.T, loggedIn bool, script func(e *reviewExecution, mark func(string) string) string) *veraPane {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	withTempRoot(t)
	repo, e := reviewTestRepo(t)
	e.Review.Number = 9
	server, claims, results := reviewWatchServer(t, map[int64]*reviewExecution{7: e})
	v := &veraPane{t: t, e: e, claims: claims, results: results, marks: t.TempDir(), token: "interactive-canary"}
	// The listener pre-trusts worktrees in Claude's config: keep it private.
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	v.claudeDB = filepath.Join(claudeDir, ".claude.json")
	if err := os.WriteFile(v.claudeDB, []byte(`{"numStartups": 3, "projects": {"/elsewhere": {"hasTrustDialogAccepted": true}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	status := `{"loggedIn": true}`
	code := 0
	if !loggedIn {
		status, code = `{"loggedIn": false}`, 1
	}
	harness := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = 'auth status' ]; then echo '" + status + "'; exit " + strconv.Itoa(code) + "; fi\n" +
		script(e, v.mark)
	if err := os.WriteFile(harness, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, v.token
	cfg.Providers["claude"] = Provider{Name: "Claude", Binary: harness, LaunchTemplate: "{{.Binary}}{{ if .SkipPermissions }} --dangerously-skip-permissions{{ end }}"}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	setVeraExecutable(t, builtVibeflow(t))
	v.tm = NewTmuxManager(fmt.Sprintf("vftest-vera-ui-%d", os.Getpid()))
	t.Cleanup(func() {
		_, _ = v.tm.run("kill-server")
		// The listener still writes its root while it shuts down.
		locks, _ := filepath.Glob(filepath.Join(RootDir(), "review-runners", "*", "runner.lock"))
		for _, path := range locks {
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if lock, err := lockReviewFile(path); err == nil {
					lock.Close()
					break
				}
			}
		}
	})
	meta := SessionMeta{Name: "vera-ui", Provider: "claude", Persona: "code_reviewer", WorkingDir: repo, Vera: &veraBinding{ProjectID: 1, RepositoryLinkID: 7, GitProvider: "github", RunnerName: "interactive"}}
	meta.TmuxSession = v.tm.FullSessionName(meta.Provider, meta.Name)
	v.session = meta.TmuxSession
	if err := startVeraTmuxSession(v.tm, meta, ""); err != nil {
		t.Fatal(err)
	}
	// A dead pane stays visible, so a stopped listener's last words are checked.
	if _, err := v.tm.run("set-option", "-t", v.session, "remain-on-exit", "on"); err != nil {
		t.Fatal(err)
	}
	return v
}

func (v *veraPane) mark(name string) string { return filepath.Join(v.marks, name) }

func (v *veraPane) capture() string {
	out, _ := v.tm.run("capture-pane", "-p", "-J", "-S", "-200", "-t", v.session)
	return out
}

func (v *veraPane) keys(keys ...string) {
	v.t.Helper()
	if _, err := v.tm.run(append([]string{"send-keys", "-t", v.session}, keys...)...); err != nil {
		v.t.Fatal(err)
	}
}

func (v *veraPane) waitFor(what string, ok func() bool) {
	v.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			v.t.Fatalf("missing %s; pane:\n%s", what, v.capture())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (v *veraPane) shows(text string) func() bool {
	return func() bool { return strings.Contains(v.capture(), text) }
}

func (v *veraPane) dead() bool {
	out, _ := v.tm.run("display-message", "-p", "-t", v.session, "#{pane_dead}")
	return strings.TrimSpace(out) == "1"
}

func (v *veraPane) exists(name string) func() bool {
	return func() bool { _, err := os.Stat(v.mark(name)); return err == nil }
}

// lastReceipt is the listener's retained record of its last attempt.
func (v *veraPane) lastReceipt() reviewReceipt {
	v.t.Helper()
	var p reviewReceipt
	matches, _ := filepath.Glob(filepath.Join(RootDir(), "review-runners", "*", "last-receipt.json"))
	if len(matches) != 1 {
		v.t.Fatalf("last receipts: %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil || json.Unmarshal(data, &p) != nil {
		v.t.Fatalf("read receipt: %v", err)
	}
	return p
}

// In a Vera tmux pane the harness runs its interactive UI attached to the
// pane: it owns the terminal (and keyboard) until its result exists, then the
// listener closes it, removes the worktree and returns to listening.
func TestReviewWatchInteractiveHarnessInPane(t *testing.T) {
	var result string
	v := startVeraPane(t, true, func(e *reviewExecution, mark func(string) string) string {
		result = reviewWatchResult(t, e, 0)
		return "printf '%s\\n' \"$@\" > " + shellQuote(mark("args")) + "\n" +
			"[ -t 0 ] && [ -t 1 ] && echo tty > " + shellQuote(mark("tty")) + "\n" +
			"echo $$ > " + shellQuote(mark("pid")) + "\n" +
			// Claude's trust dialog is pre-answered for exactly this worktree.
			"grep -q \"\\\"$(pwd -P)\\\"\" \"$CLAUDE_CONFIG_DIR/.claude.json\" && echo yes > " + shellQuote(mark("trusted")) + "\n" +
			"echo 'FAKE HARNESS UI ready for input'\n" +
			"IFS= read -r line\n" +
			"printf '%s\\n' \"$line\" > " + shellQuote(mark("input")) + "\n" +
			"cat " + shellQuote(result) + " > ../../result.json\n" +
			"echo 'result written; waiting to be closed'\n" +
			"exec sleep 300\n"
	})
	v.waitFor("harness on the pane's terminal", v.exists("tty"))
	v.waitFor("harness UI in the pane", v.shows("FAKE HARNESS UI"))
	if _, err := os.Stat(v.mark("trusted")); err != nil {
		data, _ := os.ReadFile(v.claudeDB)
		t.Fatalf("worktree not pre-trusted for Claude: %s", data)
	}
	// Keys typed into the pane reach the harness: it is the foreground group.
	v.keys("hello-from-pane", "Enter")
	v.waitFor("typed input", func() bool {
		data, _ := os.ReadFile(v.mark("input"))
		return strings.TrimSpace(string(data)) == "hello-from-pane"
	})
	v.waitFor("result, worktree removal and listening again", func() bool {
		text := v.capture()
		i := strings.Index(text, "Result: clean.")
		return v.results.Load() == 1 && i >= 0 && strings.Contains(text[i:], "Review worktree removed.") && strings.Contains(text[i:], reviewListeningLine)
	})
	t.Logf("Vera pane after the review:\n%s", v.capture())
	args, _ := os.ReadFile(v.mark("args"))
	if !strings.Contains(string(args), "--dangerously-skip-permissions") || !strings.Contains(string(args), "task.txt") {
		t.Fatalf("harness launch did not use the persona template and task prompt: %q", args)
	}
	pid, _ := os.ReadFile(v.mark("pid"))
	harnessPID, _ := strconv.Atoi(strings.TrimSpace(string(pid)))
	v.waitFor("harness stopped", func() bool { return syscall.Kill(harnessPID, 0) != nil })
	if work, _ := filepath.Glob(filepath.Join(RootDir(), "review-runners", "*", "work", "*")); len(work) != 0 {
		t.Fatalf("review worktree kept: %v", work)
	}
	// Cleanup forgets the worktree again and keeps everything else.
	var config struct {
		NumStartups int                        `json:"numStartups"`
		Projects    map[string]json.RawMessage `json:"projects"`
	}
	data, _ := os.ReadFile(v.claudeDB)
	if err := json.Unmarshal(data, &config); err != nil || config.NumStartups != 3 || len(config.Projects) != 1 || config.Projects["/elsewhere"] == nil {
		t.Fatalf("Claude config after cleanup: %s", data)
	}
	if v.claims[7].Load() != 1 || strings.Contains(v.capture(), v.token) {
		t.Fatalf("claims=%d or token shown:\n%s", v.claims[7].Load(), v.capture())
	}
	// The listener has the terminal back: Ctrl-C now stops it, as its banner says.
	v.keys("C-c")
	v.waitFor("listener exit on Ctrl-C", v.dead)
}

// A harness that exits at once without a review (a crash, a login or trust
// screen answered "no") stops Vera after one failed attempt instead of
// re-claiming until the review's attempts are spent.
func TestReviewWatchInteractiveQuickExitStopsVera(t *testing.T) {
	v := startVeraPane(t, true, func(*reviewExecution, func(string) string) string {
		return "echo 'FAKE HARNESS: please log in'\nexit 3\n"
	})
	v.waitFor("Vera stopped", v.dead)
	text := v.capture()
	t.Logf("pane:\n%s", text)
	for _, want := range []string{"claude exited within 15 s without a review (exit code 3)", "it may need a login or a trust answer", "run claude and use /login", "The review stays queued"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pane missing %q", want)
		}
	}
	if strings.Contains(text[strings.Index(text, "Claimed"):], reviewListeningLine) || v.claims[7].Load() != 1 {
		t.Fatalf("Vera kept listening after a quick harness exit (claims=%d)", v.claims[7].Load())
	}
}

// Ctrl-C during a review closes the harness; the listener then offers to stop
// instead of re-claiming, and records the attempt as cancelled.
func TestReviewWatchInteractiveInterruptOffersStop(t *testing.T) {
	v := startVeraPane(t, true, func(*reviewExecution, func(string) string) string {
		return "echo 'FAKE HARNESS UI'\nexec sleep 300\n"
	})
	v.waitFor("harness UI", v.shows("FAKE HARNESS UI"))
	v.keys("C-c")
	v.waitFor("interrupt prompt", v.shows("Review interrupted. Press Ctrl-C again within 5 s to stop Vera, or wait to resume listening."))
	v.keys("C-c")
	v.waitFor("Vera stopped", v.dead)
	text := v.capture()
	if strings.Contains(text[strings.Index(text, "Review interrupted"):], reviewListeningLine) || v.claims[7].Load() != 1 {
		t.Fatalf("Vera resumed listening (claims=%d):\n%s", v.claims[7].Load(), text)
	}
	if p := v.lastReceipt(); !strings.Contains(p.Failure, "(cancelled") {
		t.Fatalf("interrupted attempt recorded as %q", p.Failure)
	}
}

// Without a login the harness would sit on its login screen until the
// deadline; Vera refuses to claim instead.
func TestReviewWatchInteractiveRefusesLoggedOutHarness(t *testing.T) {
	v := startVeraPane(t, false, func(*reviewExecution, func(string) string) string {
		return "echo 'FAKE HARNESS UI'\nexec sleep 300\n"
	})
	v.waitFor("Vera stopped", v.dead)
	text := v.capture()
	if !strings.Contains(text, "is not logged in") || !strings.Contains(text, "run claude and use /login") || v.claims[7].Load() != 0 {
		t.Fatalf("logged-out harness claimed=%d:\n%s", v.claims[7].Load(), text)
	}
}

// Stopping the listener mid-review records the attempt as cancelled, and
// the listener does not announce listening again on its way out.
func TestReviewWatchStopMidReviewIsCancelled(t *testing.T) {
	withTempRoot(t)
	repo, e := reviewTestRepo(t)
	server, claims, _ := reviewWatchServer(t, map[int64]*reviewExecution{7: e})
	marks := t.TempDir()
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\ntouch "+shellQuote(filepath.Join(marks, "started"))+"\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "stop-canary"
	cfg.Providers["claude"] = Provider{Binary: provider}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runReviewWatch(ctx, &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "stop-test")
	}()
	deadline := time.Now().Add(15 * time.Second)
	for _, err := os.Stat(filepath.Join(marks, "started")); err != nil; _, err = os.Stat(filepath.Join(marks, "started")) {
		if time.Now().After(deadline) {
			t.Fatal("review never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	text := out.String()
	if i := strings.Index(text, "Claimed"); i < 0 || strings.Contains(text[i:], reviewListeningLine) || claims[7].Load() != 1 {
		t.Fatalf("stopping listener announced listening again:\n%s", text)
	}
	matches, _ := filepath.Glob(filepath.Join(RootDir(), "review-runners", "*", "last-receipt.json"))
	data, _ := os.ReadFile(matches[0])
	var p reviewReceipt
	if json.Unmarshal(data, &p) != nil || !strings.Contains(p.Failure, "(cancelled") {
		t.Fatalf("stopped attempt recorded as %q", p.Failure)
	}
}

// Vera tells the user, once, that a harness showing a login or trust screen
// is waiting for them.
func TestWaitReviewHarnessNotice(t *testing.T) {
	done := make(chan error, 1)
	notices := 0
	go func() { time.Sleep(100 * time.Millisecond); done <- nil }()
	finished, err := waitReviewHarness(context.Background(), done, nil, 10*time.Millisecond, func() {}, func() { notices++ })
	if err != nil || finished || notices != 1 {
		t.Fatalf("err=%v finished=%v notices=%d", err, finished, notices)
	}
	written := make(chan struct{})
	close(written)
	stop := func() { done <- nil } // Closing the guard's pipe stops the harness.
	if finished, _ = waitReviewHarness(context.Background(), done, written, time.Hour, stop, func() { notices++ }); !finished || notices != 1 {
		t.Fatalf("finished=%v notices=%d", finished, notices)
	}
}
