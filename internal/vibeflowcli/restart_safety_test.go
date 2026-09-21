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

func TestPaneRecoveryKeyboard(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	if out, err := exec.Command(python, "testdata/pane_recovery.py", binary).CombinedOutput(); err != nil {
		t.Fatalf("pane recovery keyboard check: %v\n%s", err, out)
	}
}

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
	// Metadata may point at a nested directory in the same git checkout.
	subdir := filepath.Join(repo, "src")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial, err := NewStore().List()
	if err != nil || len(initial) != 1 {
		t.Fatalf("launch metadata: %v, %v", initial, err)
	}
	initial[0].WorkingDir = subdir
	if err := NewStore().Add(initial[0]); err != nil {
		t.Fatal(err)
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
	exitedPane, err := tm.paneID(meta.TmuxSession)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the server alive so replacing the only session cannot reuse %0.
	if _, err := tm.run("new-session", "-d", "-s", "keeper", "sleep 300"); err != nil {
		t.Fatal(err)
	}
	updated, err := RestartSession(meta, cfg, tm, store, cache, NewProviderRegistry(cfg))
	if err != nil {
		t.Fatal(err)
	}
	args := readArgs()
	if resumedPane, err := tm.paneID(meta.TmuxSession); err != nil || resumedPane != exitedPane {
		t.Fatalf("resume moved out of pane %q into %q: %v", exitedPane, resumedPane, err)
	}
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
	invalidIdentity := updated
	invalidIdentity.TmuxSession = ""
	if _, err := RestartSession(invalidIdentity, cfg, tm, store, cache, NewProviderRegistry(cfg)); err == nil {
		t.Fatal("missing identity was allowed to select tmux's default pane")
	}

	// Codex hints wrap at 80 columns; tmux must join them before parsing.
	codexMeta := SessionMeta{Name: "cx", Provider: "codex", TmuxSession: tm.FullSessionName("codex", "cx"), WorkingDir: repo}
	// VERBATIM shape from a real dead codex pane (codex-cli 0.154.0): colon,
	// command indented on its own line, then a trailing "Or run ..." line.
	// The old fixture was a single hand-written line codex never emits (#5176).
	codexHint := "To continue this session, run:\n  codex resume " + id +
		"\nOr run codex resume and select Initialize Vibeflow session.\n"
	if err := tm.CreateSessionWithOpts(SessionOpts{Name: "cx", Provider: "codex", WorkDir: repo, Command: "sleep 300"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.run("resize-window", "-t", codexMeta.TmuxSession, "-x", "40", "-y", "24"); err != nil {
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
	picker := NewRestartSelectModel([]SessionMeta{codexMeta}, tm)
	if !strings.Contains(picker.View(), "resumes conversation") {
		t.Fatalf("Codex restart picker lost exact identity: %s", picker.View())
	}

	// Exact Codex IDs occupy SESSION_ID so the init prompt reaches PROMPT.
	if err := os.WriteFile(cache.path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Providers["codex"] = Provider{Binary: binary, LaunchTemplate: "{{ shellQuote .Binary }}"}
	if _, err := RestartSession(codexMeta, cfg, tm, store, cache, NewProviderRegistry(cfg)); err == nil {
		t.Fatal("quoted Codex template must fail before destroying pane")
	}
	if !tm.HasSession(codexMeta.TmuxSession) {
		t.Fatal("unsupported resume template destroyed the old pane")
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

	// Other harnesses open their history picker. A cached UUID and another
	// provider's exit hint must not silently select an unrelated conversation.
	cfg.SavedEnvVars = map[string]string{"GEMINI_API_KEY": "test", "OPENAI_API_KEY": "test"}
	for provider, pickerArg := range map[string]string{
		"gemini": "/resume", "cursor": "--resume", "qwen": "--resume",
		"kiro": "--resume-picker", "copilot": "--resume",
	} {
		t.Run(provider, func(t *testing.T) {
			prov := cfg.Providers[provider]
			prov.Binary = binary
			cfg.Providers[provider] = prov
			meta := SessionMeta{
				Name: provider, Provider: provider, TmuxSession: tm.FullSessionName(provider, provider),
				WorkingDir: repo, SessionType: "vibeflow", SkipPermissions: true, ProviderConversationID: id,
			}
			if err := tm.CreateSessionWithOpts(SessionOpts{Name: provider, Provider: provider, WorkDir: repo, Command: "sleep 300"}); err != nil {
				t.Fatal(err)
			}
			if _, err := tm.run("respawn-pane", "-k", "-t", meta.TmuxSession, "printf %s "+shellQuote(codexHint)); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				dead, err := tm.run("display-message", "-p", "-t", meta.TmuxSession, "#{pane_dead}")
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(dead) == "1" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("agent did not exit")
				}
				time.Sleep(10 * time.Millisecond)
			}
			picker := NewRestartSelectModel([]SessionMeta{meta}, tm)
			if !strings.Contains(picker.View(), "fresh start (no exact conversation ID)") {
				t.Fatalf("unsupported exact resume advertised: %s", picker.View())
			}
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			pane, err := tm.paneID(meta.TmuxSession)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := restartSession(meta, cfg, tm, store, cache, NewProviderRegistry(cfg), pane)
			if err != nil {
				t.Fatal(err)
			}
			args := readArgs()
			for _, arg := range []string{id, "--continue", "--last"} {
				if strings.Contains(args, "\n"+arg+"\n") {
					t.Errorf("unbound conversation resumed: %q", args)
				}
			}
			if updated.ProviderConversationID != "" || !strings.Contains(args, "\n"+pickerArg+"\n") || strings.Contains(args, "Initialize a vibeflow session") {
				t.Errorf("recovery did not open the native history picker: %+v, %q", updated, args)
			}
			if _, err := restartSession(updated, cfg, tm, store, cache, NewProviderRegistry(cfg), pane); err == nil {
				t.Error("a second recovery killed the running agent")
			}
			if current, err := tm.paneID(meta.TmuxSession); err != nil || current != pane {
				t.Errorf("picker replaced pane %q with %q: %v", pane, current, err)
			}
			capture, err := os.ReadFile(updated.PreviousOutputPath)
			if err != nil || !strings.Contains(string(capture), id) {
				t.Errorf("old output was not preserved: %q, %v", capture, err)
			}
		})
	}
}

// TestPaneRecoveryStaysOnTheAgentPane covers the refresh path, which used to
// stamp recovery onto whatever pane was active: after the user splits an agent
// window that is their own shell, and Enter in it launched a second agent.
func TestPaneRecoveryStaysOnTheAgentPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	tm := NewTmuxManager(fmt.Sprintf("vftest-pane-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	option := func(pane, key string) string {
		t.Helper()
		// -q: an unset user option is empty, not an error.
		out, err := tm.run("show-options", "-p", "-q", "-v", "-t", pane, key)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	split := func(session string) string {
		t.Helper()
		out, err := tm.run("split-window", "-t", session, "-P", "-F", "#{pane_id}", "sleep 300")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out) // split-window leaves the new pane active
	}
	store := NewStore()
	launch := func(name string) (string, string) {
		t.Helper()
		if err := tm.CreateSessionWithOpts(SessionOpts{Name: name, Provider: "claude", WorkDir: t.TempDir(), Command: "sleep 300"}); err != nil {
			t.Fatal(err)
		}
		session := tm.FullSessionName("claude", name)
		pane, err := tm.paneID(session)
		if err != nil {
			t.Fatal(err)
		}
		return session, pane
	}

	session, agent := launch("split")
	if err := store.Add(SessionMeta{Name: "split", Provider: "claude", TmuxSession: session}); err != nil {
		t.Fatal(err)
	}
	shell := split(session)
	want, err := tm.paneRecoveryCommand()
	if err != nil {
		t.Fatal(err)
	}
	// A binary that has since moved must be repaired on the agent pane only.
	if _, err := tm.run("set-option", "-p", "-t", agent, "@vibeflow_resume", "/gone/vibeflow resume-pane"); err != nil {
		t.Fatal(err)
	}

	// Launched before recovery existed: no identity on any pane.
	legacy, legacyPane := launch("legacy")
	legacySplit, legacySplitPane := launch("legacysplit")
	// Same server, but not in this root's store.
	_, foreignPane := launch("foreign")
	for _, pane := range []string{legacyPane, legacySplitPane} {
		for _, key := range []string{"@vibeflow_session", "@vibeflow_resume"} {
			if _, err := tm.run("set-option", "-p", "-u", "-t", pane, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tm.run("set-option", "-p", "-t", foreignPane, "@vibeflow_resume", "other-root resume-pane"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{legacy, legacySplit} {
		if err := store.Add(SessionMeta{Name: strings.TrimPrefix(s, "vibeflow_claude-"), Provider: "claude", TmuxSession: s}); err != nil {
			t.Fatal(err)
		}
	}
	legacyShell := split(legacySplit)

	tm.BindAllSessionKeys()
	if err := tm.BindSessionKeys(session); err != nil {
		t.Fatal(err)
	}

	for _, pane := range []string{shell, legacyShell, legacySplitPane} {
		if got := option(pane, "@vibeflow_session"); got != "" {
			t.Errorf("pane %s in a split window was stamped as %q", pane, got)
		}
	}
	if got := option(agent, "@vibeflow_resume"); got != want {
		t.Errorf("stale recovery command was not repaired: %q", got)
	}
	if got := option(legacyPane, "@vibeflow_session"); got != legacy {
		t.Errorf("single-pane legacy session was not adopted: %q", got)
	}
	if got := option(foreignPane, "@vibeflow_resume"); got != "other-root resume-pane" {
		t.Errorf("another root's pane was restamped: %q", got)
	}
	// Restart and conversation lookup must follow the identity, not the focus.
	if got, err := tm.agentPaneID(session); err != nil || got != agent {
		t.Errorf("agent pane = %q (%v), want %q while the user's split %q is active", got, err, agent, shell)
	}
	if out, err := tm.run("list-keys", "-T", "root", "Enter"); err != nil || !strings.Contains(out, "@vibeflow_resume") {
		t.Errorf("Enter recovery binding missing: %q, %v", out, err)
	}
}
