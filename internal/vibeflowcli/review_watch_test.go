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

// In a Vera tmux pane the harness runs its interactive UI attached to the
// pane: it owns the terminal (and keyboard) until its result exists, then the
// listener closes it, removes the worktree and returns to listening.
func TestReviewWatchInteractiveHarnessInPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	withTempRoot(t)
	repo, e := reviewTestRepo(t)
	e.Review.Number = 9
	server, claims, results := reviewWatchServer(t, map[int64]*reviewExecution{7: e})
	marks := t.TempDir()
	mark := func(name string) string { return filepath.Join(marks, name) }
	harness := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shellQuote(mark("args")) + "\n" +
		"[ -t 0 ] && [ -t 1 ] && echo tty > " + shellQuote(mark("tty")) + "\n" +
		"echo $$ > " + shellQuote(mark("pid")) + "\n" +
		"echo 'FAKE HARNESS UI ready for input'\n" +
		"IFS= read -r line\n" +
		"printf '%s\\n' \"$line\" > " + shellQuote(mark("input")) + "\n" +
		"cat " + shellQuote(reviewWatchResult(t, e, 0)) + " > ../../result.json\n" +
		"echo 'result written; waiting to be closed'\n" +
		"exec sleep 300\n"
	if err := os.WriteFile(harness, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "interactive-canary"
	cfg.Providers["claude"] = Provider{Name: "Claude", Binary: harness, LaunchTemplate: "{{.Binary}}{{ if .SkipPermissions }} --dangerously-skip-permissions{{ end }}"}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	setVeraExecutable(t, builtVibeflow(t))
	tm := NewTmuxManager(fmt.Sprintf("vftest-vera-ui-%d", os.Getpid()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	meta := SessionMeta{Name: "vera-ui", Provider: "claude", Persona: "code_reviewer", WorkingDir: repo, Vera: &veraBinding{ProjectID: 1, RepositoryLinkID: 7, GitProvider: "github", RunnerName: "interactive"}}
	meta.TmuxSession = tm.FullSessionName(meta.Provider, meta.Name)
	if err := startVeraTmuxSession(tm, meta, ""); err != nil {
		t.Fatal(err)
	}
	pane := func() string {
		out, _ := tm.run("capture-pane", "-p", "-S", "-200", "-t", meta.TmuxSession)
		return out
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("missing %s; pane:\n%s", what, pane())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	exists := func(path string) func() bool {
		return func() bool { _, err := os.Stat(path); return err == nil }
	}
	waitFor("harness on the pane's terminal", exists(mark("tty")))
	waitFor("harness UI in the pane", func() bool { return strings.Contains(pane(), "FAKE HARNESS UI") })
	// Keys typed into the pane reach the harness: it is the foreground group.
	if _, err := tm.run("send-keys", "-t", meta.TmuxSession, "hello-from-pane", "Enter"); err != nil {
		t.Fatal(err)
	}
	waitFor("typed input", func() bool {
		data, _ := os.ReadFile(mark("input"))
		return strings.TrimSpace(string(data)) == "hello-from-pane"
	})
	waitFor("result, worktree removal and listening again", func() bool {
		text := pane()
		i := strings.Index(text, "Result: clean.")
		return results.Load() == 1 && i >= 0 && strings.Contains(text[i:], "Review worktree removed.") && strings.Contains(text[i:], reviewListeningLine)
	})
	t.Logf("Vera pane after the review:\n%s", pane())
	args, _ := os.ReadFile(mark("args"))
	if !strings.Contains(string(args), "--dangerously-skip-permissions") || !strings.Contains(string(args), "task.txt") {
		t.Fatalf("harness launch did not use the persona template and task prompt: %q", args)
	}
	pid, _ := os.ReadFile(mark("pid"))
	harnessPID, _ := strconv.Atoi(strings.TrimSpace(string(pid)))
	waitFor("harness stopped", func() bool { return syscall.Kill(harnessPID, 0) != nil })
	if work, _ := filepath.Glob(filepath.Join(RootDir(), "review-runners", "*", "work", "*")); len(work) != 0 {
		t.Fatalf("review worktree kept: %v", work)
	}
	if claims[7].Load() != 1 || strings.Contains(pane(), cfg.APIToken) {
		t.Fatalf("claims=%d or token shown:\n%s", claims[7].Load(), pane())
	}
	// The listener has the terminal back: Ctrl-C now stops it, as its banner says.
	if _, err := tm.run("send-keys", "-t", meta.TmuxSession, "C-c"); err != nil {
		t.Fatal(err)
	}
	waitFor("listener exit on Ctrl-C", func() bool {
		dead, _ := tm.run("display-message", "-p", "-t", meta.TmuxSession, "#{pane_dead}")
		return strings.TrimSpace(dead) == "1"
	})
}
