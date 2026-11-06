package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func vscodeTestNavigation(t *testing.T, count int) (*vscodeNavigation, []SessionMeta, map[string]string) {
	t.Helper()
	tm, _ := attachTestServer(t)
	// Keep the tests' agent separate from the workbench's selected sessions.
	dir, err := os.MkdirTemp("/tmp", "vfnav-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	store := NewStoreWithPath(filepath.Join(dir, "sessions.json"))
	metas := []SessionMeta{}
	panes := map[string]string{}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("vibeflow_test%d", i)
		if _, err := tm.run("new-session", "-d", "-s", name, "sleep 120"); err != nil {
			t.Fatal(err)
		}
		pane, err := tm.paneID(name)
		if err != nil {
			t.Fatal(err)
		}
		panes[name] = pane
		m := SessionMeta{Name: strings.TrimPrefix(name, sessionPrefix), TmuxSession: name, ProjectID: int64(i/2 + 1), Project: fmt.Sprintf("Project %d", i/2+1), Persona: "principal_engineer", WorkingDir: dir}
		if err := store.Add(m); err != nil {
			t.Fatal(err)
		}
		metas = append(metas, m)
	}
	n, err := startVSCodeNavigation(context.Background(), filepath.Join(dir, "control.sock"), tm, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := n.close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return n, metas, panes
}

func vscodeTestCall(t *testing.T, n *vscodeNavigation, request vscodeNavigationRequest) vscodeNavigationResponse {
	t.Helper()
	conn, err := net.DialTimeout("unix", n.listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response vscodeNavigationResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestVSCodeNavigation_SocketSwitchesProjectsAndPersonasWithoutRestarting(t *testing.T) {
	n, metas, panes := vscodeTestNavigation(t, 4)
	if err := n.open(vscodeNavigationRequest{Version: 1, View: "project", Session: metas[0].TmuxSession}); err != nil {
		t.Fatal(err)
	}
	// A refresh already in flight when composition starts must not persist
	// the internal holder as a new user agent.
	(Model{tmux: n.tmux, store: n.store}).refreshSessions()
	stored, err := n.store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range stored {
		if isWorkbenchHolder(meta.TmuxSession) {
			t.Fatal("workbench leaked into the fleet store")
		}
	}
	go n.serve()
	if info, err := os.Stat(n.listener.Addr().String()); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	for _, index := range []int{1, 3, 0, 0, 2} {
		name := metas[index].TmuxSession
		for _, view := range []string{"session", "session", "project", "project"} {
			res := vscodeTestCall(t, n, vscodeNavigationRequest{Version: 1, View: view, Session: name})
			if !res.OK {
				t.Fatalf("%s %s: %s", view, name, res.Error)
			}
			out, err := n.tmux.run("display-message", "-p", "-t", n.comp.holder, "#{pane_id}|#{window_zoomed_flag}")
			wantZoom := "0"
			if view == "session" {
				wantZoom = "1"
			}
			if err != nil || strings.TrimSpace(out) != panes[name]+"|"+wantZoom {
				t.Fatalf("view %s: %q %v", view, out, err)
			}
		}
	}
	for name, pane := range panes {
		out, err := n.tmux.run("show-options", "-p", "-v", "-t", pane, "@vibeflow_session")
		if err != nil || strings.TrimSpace(out) != name {
			t.Fatalf("origin for %s: %q %v", name, out, err)
		}
	}
	if err := n.restore(); err != nil {
		t.Fatal(err)
	}
	for name, pane := range panes {
		got, err := n.tmux.paneID(name)
		if err != nil || got != pane || n.tmux.paneDead(got) {
			t.Fatalf("agent %s changed or died: %s, want %s, %v", name, got, pane, err)
		}
	}
}

func TestVSCodeNavigation_SingleSessionAndNewAgentJoin(t *testing.T) {
	n, metas, panes := vscodeTestNavigation(t, 1)
	if err := n.open(vscodeNavigationRequest{Version: 1, View: "session", Session: metas[0].TmuxSession}); err != nil {
		t.Fatal(err)
	}
	name := "vibeflow_later"
	if _, err := n.tmux.run("new-session", "-d", "-s", name, "sleep 120"); err != nil {
		t.Fatal(err)
	}
	pane, _ := n.tmux.paneID(name)
	if err := n.store.Add(SessionMeta{Name: "later", TmuxSession: name, Project: "New project", ProjectID: 9}); err != nil {
		t.Fatal(err)
	}
	go n.serve()
	res := vscodeTestCall(t, n, vscodeNavigationRequest{Version: 1, View: "session", Session: name})
	if !res.OK {
		t.Fatal(res.Error)
	}
	if err := n.restore(); err != nil {
		t.Fatal(err)
	}
	panes[name] = pane
	for name, pane := range panes {
		got, err := n.tmux.paneID(name)
		if err != nil || got != pane {
			t.Fatalf("restore %s: %s != %s, %v", name, got, pane, err)
		}
	}
}

func TestVSCodeNavigation_RejectsUnknownTargetsAndPreservesExistingWorkbench(t *testing.T) {
	n, metas, panes := vscodeTestNavigation(t, 2)
	if err := n.open(vscodeNavigationRequest{Version: 1, View: "project", Session: "vibeflow_not_registered"}); err == nil {
		t.Fatal("accepted unknown session")
	}
	if err := n.open(vscodeNavigationRequest{Version: 1, View: "project", Session: metas[0].TmuxSession}); err != nil {
		t.Fatal(err)
	}
	go n.serve()
	for _, req := range []vscodeNavigationRequest{
		{Version: 2, View: "session", Session: metas[0].TmuxSession},
		{Version: 1, View: "kill", Session: metas[0].TmuxSession},
		{Version: 1, View: "session", Session: "vibeflow_agent"}, // live, but not in this store
	} {
		if res := vscodeTestCall(t, n, req); res.OK {
			t.Fatalf("accepted %+v", req)
		}
	}
	if _, err := n.tmux.composeProjectWorkbench([]WorkbenchProject{{Label: "bad", Sessions: []string{"vibeflow_agent"}}}, "", nil, true); err == nil {
		t.Fatal("replaced existing workbench")
	}
	for _, pane := range panes {
		if n.tmux.paneDead(pane) {
			t.Fatal("existing workbench agent killed")
		}
	}
}

func TestVSCodeNavigation_ConcurrentClicksAndRestoreAreSerialized(t *testing.T) {
	n, metas, panes := vscodeTestNavigation(t, 2)
	if err := n.open(vscodeNavigationRequest{Version: 1, View: "project", Session: metas[0].TmuxSession}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n.mu.Lock()
			defer n.mu.Unlock()
			if err := n.focusLocked(vscodeNavigationRequest{View: "session", Session: metas[i%2].TmuxSession}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if err := n.restore(); err != nil {
		t.Fatal(err)
	}
	for name, pane := range panes {
		got, err := n.tmux.paneID(name)
		if err != nil || got != pane {
			t.Fatalf("%s: %s %v", name, got, err)
		}
	}
}

func TestVSCodeNavigation_RestoreDoesNotRecreateStoppedSession(t *testing.T) {
	n, metas, panes := vscodeTestNavigation(t, 2)
	if err := n.open(vscodeNavigationRequest{Version: 1, View: "project", Session: metas[0].TmuxSession}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.tmux.run("kill-pane", "-t", panes[metas[1].TmuxSession]); err != nil {
		t.Fatal(err)
	}
	if err := n.restore(); err != nil {
		t.Fatal(err)
	}
	if n.tmux.HasSession(metas[1].TmuxSession) {
		t.Fatal("restore recreated the stopped agent as an empty shell")
	}
	got, err := n.tmux.paneID(metas[0].TmuxSession)
	if err != nil || got != panes[metas[0].TmuxSession] {
		t.Fatal("the remaining agent changed")
	}
}
