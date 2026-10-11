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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// attachTestServer starts a real tmux server on a private socket with one
// session and returns its manager and that session's pane tty.
func attachTestServer(t *testing.T) (*TmuxManager, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager(fmt.Sprintf("vfattach%d", os.Getpid()))
	if _, err := tm.run("new-session", "-d", "-s", "vibeflow_agent", "sleep 60"); err != nil {
		t.Fatalf("start tmux server: %v", err)
	}
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	tty, err := tm.run("display-message", "-p", "-t", "vibeflow_agent", "#{pane_tty}")
	if err != nil {
		t.Fatalf("read pane tty: %v", err)
	}
	return tm, strings.TrimSpace(tty)
}

func TestIsPaneTTY_MatchesOnlyThisServersPanes(t *testing.T) {
	tm, paneTTY := attachTestServer(t)
	pane, err := os.Stat(paneTTY)
	if err != nil {
		t.Fatalf("stat pane tty: %v", err)
	}
	if !tm.isPaneTTY(pane) {
		t.Errorf("pane tty %s not recognised as a pane", paneTTY)
	}
	devNull, err := os.Stat(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if tm.isPaneTTY(devNull) {
		t.Error("/dev/null recognised as a pane")
	}
	if NewTmuxManager("vfattach-no-server").isPaneTTY(pane) {
		t.Error("pane recognised on a socket with no server")
	}
}

// The #5837 bug: a terminal launched from a pane (VS Code started from an
// agent pane) inherits $TMUX but is not a pane, so switch-client has no client
// to move. Such a terminal must attach instead.
func TestAttachSessionCmd_InheritedTMUXOutsidePane_AttachesSession(t *testing.T) {
	tm, _ := attachTestServer(t)
	inherited, err := tm.run("display-message", "-p", "#{socket_path},#{pid},0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", strings.TrimSpace(inherited))
	if got := tm.AttachSessionCmd("agent").Args; got[3] != "attach-session" {
		t.Errorf("inherited TMUX: args = %v, want attach-session", got)
	}
	t.Setenv("TMUX", "")
	if got := tm.AttachSessionCmd("agent").Args; got[3] != "attach-session" {
		t.Errorf("no TMUX: args = %v, want attach-session", got)
	}
}

// A TUI running in one of the server's own panes keeps switching its client.
func TestAttachSessionCmd_InsidePane_SwitchesClient(t *testing.T) {
	tm, _ := attachTestServer(t)
	out := filepath.Join(t.TempDir(), "args")
	helper := fmt.Sprintf("VIBEFLOW_ATTACH_HELPER_OUT=%s %s -test.run='^TestAttachSessionCmdHelperProcess$'", out, os.Args[0])
	if _, err := tm.run("new-session", "-d", "-s", "vibeflow_tui", helper); err != nil {
		t.Fatalf("start TUI pane: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if got, err := os.ReadFile(out); err == nil && len(got) > 0 {
			if !strings.Contains(string(got), " switch-client ") {
				t.Errorf("args inside pane = %q, want switch-client", got)
			}
			return
		}
	}
	t.Fatal("helper in the tmux pane wrote no args within 10s")
}

// A failed attach used to vanish: Enter did nothing and nothing was logged.
func TestUpdate_AttachExitWithError_LogsAndShowsError(t *testing.T) {
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	logger := NewLogger()
	next, _ := Model{logger: logger}.Update(attachExitMsg{err: errors.New("exit status 1: no current client")})
	if err := next.(Model).err; err == nil || !strings.Contains(err.Error(), "no current client") {
		t.Errorf("m.err = %v, want the attach error", err)
	}
	if logged, _ := os.ReadFile(logger.path); !strings.Contains(string(logged), "[ERROR] attach: exit status 1: no current client") {
		t.Errorf("log = %q, want the attach error", logged)
	}
}

// TestAttachSessionCmdHelperProcess runs inside a tmux pane for
// TestAttachSessionCmd_InsidePane_SwitchesClient and records the command.
func TestAttachSessionCmdHelperProcess(t *testing.T) {
	out := os.Getenv("VIBEFLOW_ATTACH_HELPER_OUT")
	if out == "" {
		t.Skip("helper process only")
	}
	socket := filepath.Base(strings.SplitN(os.Getenv("TMUX"), ",", 2)[0])
	args := NewTmuxManager(socket).AttachSessionCmd("agent").Args
	if err := os.WriteFile(out, []byte(strings.Join(args, " ")), 0o600); err != nil {
		t.Fatal(err)
	}
}
