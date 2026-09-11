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

// Exercise the launch/restart entry points using a real tmux server and an
// agent that records its arguments without making network calls.
func TestLaunchAndRestartDirectorySafety(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "topic")
	t.Chdir(repo)
	if out, err := exec.Command("git", "checkout", "topic").CombinedOutput(); err != nil {
		t.Fatalf("checkout topic: %v: %s", err, out)
	}
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	socket := fmt.Sprintf("vftest-resume-%d-%d", os.Getpid(), time.Now().UnixNano())
	tm := NewTmuxManager(socket)
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	record := filepath.Join(state, "agent-args")
	binary := filepath.Join(state, "fake-agent")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.TmuxSocket = socket
	cfg.Providers["claude"] = Provider{Binary: binary, LaunchTemplate: "{{.Binary}}"}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	cmd := launchCmd()
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	readArgs := func() string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(record); err == nil && len(data) > 0 {
				return string(data)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("agent did not record arguments")
		return ""
	}
	readArgs()
	if got := GetGitBranch(repo); got != "topic" {
		t.Fatalf("launch without --branch changed checkout to %q", got)
	}
	// A second launch must not move the first agent's checkout underneath it.
	second := launchCmd()
	second.SilenceErrors, second.SilenceUsage = true, true
	second.SetArgs([]string{"--branch", "main"})
	if err := second.Execute(); err == nil {
		t.Fatal("branch switch under a running peer was allowed")
	}
	if got := GetGitBranch(repo); got != "topic" {
		t.Fatalf("running peer moved to %q", got)
	}
	store, cache := NewStore(), NewSessionCache()
	all, err := store.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("launched sessions = %v, %v", all, err)
	}
	meta := all[0]
	if !filepath.IsAbs(meta.WorkingDir) {
		t.Fatalf("stored workdir must remain meaningful after changing cwd: %q", meta.WorkingDir)
	}
	// A solitary session is insufficient evidence: a manual provider run or
	// another CLI root can own the most recent conversation in this directory.
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if _, err := RestartSession(meta, cfg, tm, store, cache, NewProviderRegistry(cfg)); err != nil {
		t.Fatal(err)
	}
	// Let the restarted process write its arguments before replacing it again.
	readArgs()
	if args := readArgs(); strings.Contains(args, "--continue") {
		t.Fatalf("unbound latest conversation resumed: %q", args)
	}

	const id = "7ae74319-242d-45d1-b251-0495f225448c"
	hint := "Resume this session with:\nclaude --resume " + id + "\n"
	if _, err := tm.run("respawn-pane", "-k", "-t", meta.TmuxSession, "printf %s "+shellQuote(hint)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for tm.ResumeConversationID(meta) != id && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := tm.ResumeConversationID(meta); got != id {
		output, _ := tm.run("capture-pane", "-p", "-J", "-t", meta.TmuxSession, "-S", "-30")
		dead, err := tm.run("display-message", "-p", "-t", meta.TmuxSession, "#{pane_dead}")
		t.Fatalf("dead pane ID = %q, pane_dead=%q err=%v, output=%q", got, dead, err, output)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	updated, err := RestartSession(meta, cfg, tm, store, cache, NewProviderRegistry(cfg))
	if err != nil {
		t.Fatal(err)
	}
	args := readArgs()
	if !strings.Contains(args, "\n--resume\n"+id+"\n") {
		t.Fatalf("exact resume missing: %q", args)
	}
	if updated.ProviderConversationID != id {
		t.Errorf("conversation ID not persisted: %+v", updated)
	}
	capture, err := os.ReadFile(updated.PreviousOutputPath)
	if err != nil || !strings.Contains(string(capture), id) {
		t.Fatalf("exit hint not recoverable: %q, %v", capture, err)
	}
	info, err := os.Stat(updated.PreviousOutputPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("recovery file must be private: %v, %v", info, err)
	}
	stored, found, err := store.Get(meta.Name)
	if err != nil || !found || stored.ProviderConversationID != id {
		t.Fatalf("stored ID = %+v, %v", stored, err)
	}
	// Existing live panes invalidate cached identity after /new or /clear.
	if got := tm.ResumeConversationID(updated); got != "" {
		t.Fatalf("live pane reused stale ID %q", got)
	}
	const newerID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	newHint := "Resume this session with:\nclaude --resume " + newerID + "\n"
	if _, err := tm.run("respawn-pane", "-k", "-t", meta.TmuxSession, "printf %s "+shellQuote(newHint)); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for tm.ResumeConversationID(updated) != newerID && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := tm.ResumeConversationID(updated); got != newerID {
		t.Fatalf("new final hint lost to stale ID: %q", got)
	}
	if err := os.WriteFile(cache.path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestartSession(updated, cfg, tm, store, cache, NewProviderRegistry(cfg)); err == nil {
		t.Fatal("restart must fail when recovery metadata cannot be saved")
	}
	if !tm.HasSession(meta.TmuxSession) {
		t.Fatal("failed metadata write destroyed the original pane")
	}
	invalidDir := updated
	invalidDir.WorkingDir = "."
	if _, err := RestartSession(invalidDir, cfg, tm, store, cache, NewProviderRegistry(cfg)); err == nil {
		t.Fatal("relative legacy workdir was resolved against an unrelated cwd")
	}
	if !tm.HasSession(meta.TmuxSession) {
		t.Fatal("invalid workdir destroyed the original pane")
	}

	// Codex hints wrap at 80 columns; tmux must join them before parsing.
	codexMeta := SessionMeta{Name: "cx", Provider: "codex", TmuxSession: tm.FullSessionName("codex", "cx"), WorkingDir: repo}
	codexHint := "To continue this session, run codex resume " + id + "\n"
	if err := tm.CreateSessionWithOpts(SessionOpts{Name: "cx", Provider: "codex", WorkDir: repo, Command: "sleep 300"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.run("resize-window", "-t", codexMeta.TmuxSession, "-x", "60", "-y", "24"); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.run("respawn-pane", "-k", "-t", codexMeta.TmuxSession, "printf %s "+shellQuote(codexHint)); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for tm.ResumeConversationID(codexMeta) != id && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := tm.ResumeConversationID(codexMeta); got != id {
		t.Fatalf("wrapped Codex hint ID = %q", got)
	}

	// Exact Codex IDs occupy SESSION_ID so the init prompt reaches PROMPT.
	if err := os.WriteFile(cache.path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Providers["codex"] = Provider{Binary: binary, LaunchTemplate: "{{.Binary}}"}
	codexMeta.SessionType = "vibeflow"
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if _, err := RestartSession(codexMeta, cfg, tm, store, cache, NewProviderRegistry(cfg)); err != nil {
		t.Fatal(err)
	}
	if args := readArgs(); !strings.Contains(args, "\nresume\n"+id+"\nInitialize a vibeflow session") {
		t.Fatalf("Codex positional arguments = %q", args)
	}

}
