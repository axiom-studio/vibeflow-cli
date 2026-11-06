package vibeflowcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// This protocol is enabled only by --client=vscode. The extension creates a
// private directory per terminal and passes its socket path as an argv value.
// No commands, prompts, keystrokes, or shell fragments are accepted.
type vscodeNavigationRequest struct {
	Version int    `json:"version"`
	View    string `json:"view"`              // ping, project, session
	Session string `json:"session,omitempty"` // exact full tmux name, never a persona label
}

type vscodeNavigationResponse struct {
	Version    int    `json:"version"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Root       string `json:"root"`
	TmuxSocket string `json:"tmux_socket"`
}

type vscodeNavigateMsg struct {
	request vscodeNavigationRequest
	ctx     context.Context
	reply   chan error
}

type vscodeReadyMsg struct {
	err   error
	reply chan error
}

type vscodeExitMsg struct{ err error }

// The socket goroutine can focus existing panes while Bubble Tea is paused in
// ExecProcess. Only the TUI starts an attach. This mutex serializes focus,
// composition and restoration, so a rapid click cannot rebuild a live layout.
type vscodeNavigation struct {
	mu       sync.Mutex
	ctx      context.Context
	tmux     *TmuxManager
	store    *Store
	comp     *WorkbenchComposition
	windows  map[string]string
	send     func(tea.Msg)
	done     chan struct{}
	listener net.Listener
	closed   bool
}

func startVSCodeNavigation(ctx context.Context, socketPath string, tmux *TmuxManager, store *Store) (*vscodeNavigation, error) {
	if !filepath.IsAbs(socketPath) || len(socketPath) > 100 {
		return nil, fmt.Errorf("VS Code control socket must be an absolute path of at most 100 bytes")
	}
	parent, err := os.Lstat(filepath.Dir(socketPath))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("VS Code control socket requires a private directory (mode 0700)")
	}
	// Listen refuses existing paths. Never unlink another terminal's socket.
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on VS Code control socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("protect VS Code control socket: %w", err)
	}
	n := &vscodeNavigation{ctx: ctx, tmux: tmux, store: store, done: make(chan struct{}), listener: listener}
	return n, nil
}

func (n *vscodeNavigation) serve() {
	// One request at a time bounds resource usage and preserves click order.
	for {
		conn, err := n.listener.Accept()
		if err != nil {
			return
		}
		n.serveConnection(conn)
		conn.Close()
	}
}

func (n *vscodeNavigation) serveConnection(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	var req vscodeNavigationRequest
	decoder := json.NewDecoder(io.LimitReader(conn, 4096))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
		defer cancel()
		err = n.navigate(ctx, req)
	}
	root, _ := filepath.Abs(RootDir())
	res := vscodeNavigationResponse{Version: 1, OK: err == nil, Root: root, TmuxSocket: n.tmux.socketName}
	if err != nil {
		res.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(res) // Disconnects do not affect the agent panes.
}

func (n *vscodeNavigation) navigate(ctx context.Context, req vscodeNavigationRequest) error {
	if req.Version != 1 {
		return fmt.Errorf("unsupported VS Code navigation protocol version")
	}
	if req.View == "ping" {
		return nil
	}
	if (req.View != "session" && req.View != "project") || !strings.HasPrefix(req.Session, sessionPrefix) || len(req.Session) > 512 {
		return fmt.Errorf("navigation needs a project or session view and an exact local session name")
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return fmt.Errorf("VS Code CLI is closing")
	}
	if n.comp != nil {
		err := n.focusLocked(req)
		n.mu.Unlock()
		return err
	}
	n.mu.Unlock()

	msg := vscodeNavigateMsg{request: req, ctx: ctx, reply: make(chan error, 1)}
	go n.send(msg) // Program.Send stops when the program exits; the request context cancels stale clicks.
	select {
	case err := <-msg.reply:
		return err
	case <-ctx.Done():
		return fmt.Errorf("CLI navigation timed out: %w", ctx.Err())
	case <-n.done:
		return fmt.Errorf("VS Code CLI closed")
	}
}

// Project IDs win. Older name-only records join an ID group only when the
// name maps unambiguously, matching Agent Fleet's grouping rules.
func vscodeProjectKeys(metas []SessionMeta) map[string]string {
	ids := map[string]int64{}
	for _, m := range metas {
		if m.ProjectID == 0 || m.Project == "" {
			continue
		}
		name := strings.ToLower(m.Project)
		if id, ok := ids[name]; ok && id != m.ProjectID {
			ids[name] = -1
		} else if !ok {
			ids[name] = m.ProjectID
		}
	}
	keys := make(map[string]string, len(metas))
	for _, m := range metas {
		id := m.ProjectID
		if id == 0 {
			id = ids[strings.ToLower(m.Project)]
		}
		key := "name:" + m.Project
		if id > 0 {
			key = fmt.Sprintf("id:%d", id)
		} else if m.Project == "" {
			key = "dir:" + m.WorkingDir
		}
		keys[m.TmuxSession] = key
	}
	return keys
}

func (n *vscodeNavigation) open(req vscodeNavigationRequest) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return fmt.Errorf("VS Code CLI is closing")
	}
	if n.comp != nil {
		return fmt.Errorf("VS Code workbench is already opening; retry navigation")
	}
	metas, err := n.store.List()
	if err != nil {
		return fmt.Errorf("read local sessions for navigation: %w", err)
	}
	keys := vscodeProjectKeys(metas)
	projects := []WorkbenchProject{}
	indexes := map[string]int{}
	titles := map[string]string{}
	found := false
	for _, m := range metas {
		if !strings.HasPrefix(m.TmuxSession, sessionPrefix) || isWorkbenchHolder(m.TmuxSession) {
			continue
		}
		pane, err := n.tmux.agentPaneID(m.TmuxSession)
		if err != nil || n.tmux.paneDead(pane) {
			continue
		}
		if m.TmuxSession == req.Session {
			found = true
		}
		key := keys[m.TmuxSession]
		i, ok := indexes[key]
		if !ok {
			i = len(projects)
			indexes[key] = i
			label := m.Project
			if label == "" {
				label = filepath.Base(m.WorkingDir)
			}
			projects = append(projects, WorkbenchProject{Label: sanitizeWorkbenchTitle(label)})
		}
		projects[i].Sessions = append(projects[i].Sessions, m.TmuxSession)
		titles[m.TmuxSession] = workbenchHeader(m.Persona, m.Project, m.Branch)
	}
	if !found {
		return fmt.Errorf("session %q is no longer running in this CLI root", req.Session)
	}
	comp, err := n.tmux.composeProjectWorkbench(projects, "", titles, true)
	if err != nil {
		return fmt.Errorf("open VS Code workbench: %w", err)
	}
	n.comp = comp
	n.windows = map[string]string{}
	for _, src := range comp.sources {
		win, err := n.tmux.windowID(src.paneID)
		if err == nil {
			n.windows[keys[src.name]] = win
		}
	}
	if err := n.focusLocked(req); err != nil {
		return errors.Join(err, n.restoreLocked())
	}
	return nil
}

func (n *vscodeNavigation) focusLocked(req vscodeNavigationRequest) error {
	if req.View == "project" {
		if err := n.refreshProjectLocked(req.Session); err != nil {
			return err
		}
	}
	var pane string
	for _, src := range n.comp.sources {
		if src.name == req.Session {
			pane = src.paneID
			break
		}
	}
	if pane == "" {
		var err error
		pane, err = n.addSessionLocked(req.Session)
		if err != nil {
			return err
		}
	}
	if n.tmux.paneDead(pane) {
		return fmt.Errorf("session %q has exited; return to the CLI menu to restart it", req.Session)
	}
	win, err := n.tmux.windowID(pane)
	if err != nil {
		return fmt.Errorf("find session %q in workbench: %w", req.Session, err)
	}
	if _, err := n.tmux.run("select-window", "-t", win); err != nil {
		return fmt.Errorf("select project window: %w", err)
	}
	// tmux -Z toggles. Read the state first so repeated clicks are idempotent.
	zoom, err := n.tmux.run("display-message", "-p", "-t", pane, "#{window_zoomed_flag}")
	if err != nil {
		return fmt.Errorf("read project zoom state: %w", err)
	}
	if strings.TrimSpace(zoom) == "1" {
		if _, err := n.tmux.run("resize-pane", "-Z", "-t", pane); err != nil {
			return fmt.Errorf("show project panes: %w", err)
		}
	}
	if _, err := n.tmux.run("select-pane", "-t", pane); err != nil {
		return fmt.Errorf("focus session pane: %w", err)
	}
	if req.View == "session" {
		if _, err := n.tmux.run("resize-pane", "-Z", "-t", pane); err != nil {
			return fmt.Errorf("focus session view: %w", err)
		}
	}
	return nil
}

// Refresh only the selected project. Other windows and the current agent's
// running process are left intact when a new agent joins the fleet.
func (n *vscodeNavigation) refreshProjectLocked(anchor string) error {
	metas, err := n.store.List()
	if err != nil {
		return fmt.Errorf("read project sessions: %w", err)
	}
	keys := vscodeProjectKeys(metas)
	key, exists := keys[anchor]
	if !exists {
		return fmt.Errorf("session %q does not belong to this CLI root", anchor)
	}
	joined := map[string]bool{}
	for _, src := range n.comp.sources {
		joined[src.name] = true
	}
	for _, meta := range metas {
		if keys[meta.TmuxSession] != key || joined[meta.TmuxSession] {
			continue
		}
		pane, err := n.tmux.agentPaneID(meta.TmuxSession)
		if err != nil || n.tmux.paneDead(pane) {
			continue
		}
		if _, err := n.addSessionLocked(meta.TmuxSession); err != nil {
			return err
		}
	}
	return nil
}

// Agents launched while the workbench is open join it without replacing any
// existing pane. The persistent sources list also covers their later restore.
func (n *vscodeNavigation) addSessionLocked(name string) (string, error) {
	metas, err := n.store.List()
	if err != nil {
		return "", fmt.Errorf("read new local session: %w", err)
	}
	keys := vscodeProjectKeys(metas)
	for _, m := range metas {
		if m.TmuxSession != name {
			continue
		}
		pane, err := n.tmux.agentPaneID(name)
		if err != nil || n.tmux.paneDead(pane) {
			return "", fmt.Errorf("session %q is no longer running in this CLI root", name)
		}
		win := n.windows[keys[name]]
		placeholder := ""
		if win == "" {
			out, err := n.tmux.run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", n.comp.holder, "-n", sanitizeWorkbenchTitle(m.Project))
			if err != nil {
				return "", fmt.Errorf("add project to workbench: %w", err)
			}
			win = strings.TrimSpace(out)
			placeholder, err = n.tmux.paneID(win)
			if err != nil {
				return "", err
			}
		}
		titles := map[string]string{name: workbenchHeader(m.Persona, m.Project, m.Branch)}
		if err := n.tmux.composeInto(win, []string{name}, titles, n.comp); err != nil {
			if placeholder != "" {
				_, _ = n.tmux.run("kill-pane", "-t", placeholder)
			}
			return "", err
		}
		if placeholder != "" {
			_, _ = n.tmux.run("kill-pane", "-t", placeholder)
		}
		n.windows[keys[name]] = win
		return pane, nil
	}
	return "", fmt.Errorf("session %q does not belong to this CLI root", name)
}

func (n *vscodeNavigation) restoreLocked() error {
	if n.comp == nil {
		return nil
	}
	if err := n.comp.Restore(); err != nil {
		return fmt.Errorf("restore VS Code workbench (panes preserved): %w", err)
	}
	n.comp = nil
	n.windows = nil
	return nil
}

func (n *vscodeNavigation) restore() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.restoreLocked()
}

func (n *vscodeNavigation) close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.closed {
		n.closed = true
		close(n.done)
		n.listener.Close()
	}
	return n.restoreLocked()
}

func (m Model) vscodeOpenCmd(req vscodeNavigationRequest, reply chan error) tea.Cmd {
	return func() tea.Msg {
		return vscodeReadyMsg{err: m.vscode.open(req), reply: reply}
	}
}

func (m Model) vscodeProjectCmd() tea.Cmd {
	_, names := m.selectedProjectSessions()
	if len(names) == 0 {
		return nil
	}
	return m.vscodeOpenCmd(vscodeNavigationRequest{Version: 1, View: "project", Session: m.tmux.ensurePrefix(names[0])}, nil)
}

func (n *vscodeNavigation) attachCommand() *exec.Cmd {
	base := n.tmux.AttachSessionCmd(workbenchHolderName)
	cmd := exec.CommandContext(n.ctx, base.Path, base.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = base.Stdin, base.Stdout, base.Stderr, base.Env
	return cmd
}
