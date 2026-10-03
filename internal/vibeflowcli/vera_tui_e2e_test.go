//go:build darwin || linux

package vibeflowcli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Drives Vera end to end through the real binary in a real PTY with real tmux:
// the default binary starts straight into the session list, New Agent > Vera > Provider >
// Confirm creates an ordinary tmux session whose listener registers only the
// chosen repository and idles without a model, the session survives quitting
// the TUI, and deleting it with d deregisters and stops the listener.
func TestVeraTUIBinaryPickerLifecycle(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("native script command unavailable; real PTY is required")
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	binary := builtVibeflow(t)
	repo, _ := reviewTestRepo(t) // origin acme/repo -> link 7
	repo2, _ := reviewTestRepo(t)
	reviewTestGit(t, repo2, "remote", "set-url", "origin", "https://github.com/acme/two.git") // link 8
	launchRepo, _ := reviewTestRepo(t)
	reviewTestGit(t, launchRepo, "remote", "set-url", "origin", "https://github.com/acme/cli.git")
	root, binDir, marks := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Symlink(tmuxPath, filepath.Join(binDir, "tmux")); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("vera-tui-%d", os.Getpid())
	tmux := func(args ...string) string {
		out, _ := exec.Command(tmuxPath, append([]string{"-L", socket}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { tmux("kill-server") })
	modelRun := filepath.Join(marks, "claude-model")
	provider := filepath.Join(binDir, "claude")
	// Like the real CLI, `auth status` answers without a model; anything else
	// would be a model run.
	claudeScript := "#!/bin/sh\n[ \"$*\" = 'auth status' ] && { echo '{\"loggedIn\": true}'; exit 0; }\nprintf '%s\\n' \"$*\" >> " + shellQuote(modelRun) + "\nexit 1\n"
	if err := os.WriteFile(provider, []byte(claudeScript), 0700); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	registered := map[int64]int{}     // repository_link_id -> registrations
	capabilities := map[int64]any{}   // repository_link_id -> capabilities sent
	runnerLinks := map[string]int64{} // runner id -> repository_link_id
	var heartbeats, polls, deletes, repositoryReads int
	var deleted []string
	counts := func() (int, int, int, int) {
		mu.Lock()
		defer mu.Unlock()
		return heartbeats, polls, deletes, repositoryReads
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			return
		}
		if r.Header.Get("Authorization") != "Bearer vera-api-canary" {
			t.Errorf("request without the configured API credential: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/rest/v1/vibeflow")
		parts := strings.Split(path, "/") // "", projects, 66, pr-review-runners, <id>, <action>
		switch {
		case r.Method == "GET" && path == "/projects":
			fmt.Fprint(w, `[{"id":66,"name":"Axiom"}]`)
		case r.Method == "GET" && path == "/projects/66/pr-review-repositories":
			repositoryReads++
			fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"github.com","repository_link_id":8,"repository_name":"acme/two"}],"supported_runner_capabilities":["repository_review_v1"]}`)
		case r.Method == "GET" && path == "/projects/66/sessions":
			fmt.Fprint(w, `[]`)
		case r.Method == "GET" && path == "/projects/66/pr-review-sessions":
			// Two reviews for Vera's repository (link 7), one for another;
			// the server filters by repository when asked.
			link := r.URL.Query().Get("repository_link_id")
			var sessions []string
			for _, s := range []struct {
				job  string
				link int64
			}{{"job-5", 7}, {"job-4", 7}, {"job-34", 8}} {
				if link == "" || link == strconv.FormatInt(s.link, 10) {
					sessions = append(sessions, fmt.Sprintf(`{"session_id":"s-%s","project_id":66,"job_id":%q,"provider":"github","repository_link_id":%d,"state":"completed"}`, s.job, s.job, s.link))
				}
			}
			fmt.Fprintf(w, `{"sessions":[%s]}`, strings.Join(sessions, ","))
		case r.Method == "GET" && path == "/projects/66/pr-review-summaries/job-4":
			fmt.Fprint(w, veraE2ESummary("job-4", 7, 4, "Add late payment fee calculation"))
		case r.Method == "GET" && path == "/projects/66/pr-review-summaries/job-5":
			fmt.Fprint(w, veraE2ESummary("job-5", 7, 5, "Fix rounding"))
		case r.Method == "GET" && path == "/projects/66/pr-review-summaries/job-4/findings":
			fmt.Fprint(w, `{"findings":[{"id":"f1","job_id":"job-4","title":"Rounding drops cents","severity":"high","path":"billing/fee.go","line":42,"state":"present","trigger":"fee of 10.005","impact":"undercharged","evidence":"math.Floor","verification":"go test ./billing"}]}`)
		case r.Method == "POST" && path == "/projects/66/pr-review-runners":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			link, _ := body["repository_link_id"].(float64)
			id, _ := body["id"].(string)
			registered[int64(link)]++
			capabilities[int64(link)] = body["capabilities"]
			runnerLinks[id] = int64(link)
			body["user_id"] = 42
			_ = json.NewEncoder(w).Encode(body)
		case len(parts) == 6 && r.Method == "POST" && parts[3] == "pr-review-runners" && parts[5] == "heartbeat":
			if _, ok := runnerLinks[parts[4]]; !ok {
				t.Errorf("heartbeat from unregistered runner %s", parts[4])
			}
			heartbeats++
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 6 && r.Method == "GET" && parts[3] == "pr-review-runners" && parts[5] == "work":
			if _, ok := runnerLinks[parts[4]]; !ok {
				t.Errorf("work poll from unregistered runner %s", parts[4])
			}
			polls++
			fmt.Fprint(w, `{"reviews":[]}`)
		case len(parts) == 5 && r.Method == "DELETE" && parts[3] == "pr-review-runners":
			deletes++
			deleted = append(deleted, parts[4])
			w.WriteHeader(http.StatusNoContent)
		default:
			// Includes /sessions/init and /sessions/register: Vera must never
			// take the coding-agent session path.
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	// The listener runs inside tmux, so find it by its unique --root.
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		roots = append(roots, resolved)
	}
	processes := func(history bool) []int {
		out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "args=").Output()
		if err != nil {
			t.Fatalf("ps: %v", err)
		}
		var pids []int
		for _, line := range strings.Split(string(out), "\n") {
			for _, r := range roots {
				// The tmux server keeps its first client's argv, so match the binary.
				if fields := strings.Fields(line); len(fields) > 1 && fields[1] == binary && strings.Contains(line, "--root "+r+" --config") && strings.Contains(line, " review-watch ") && strings.Contains(line, " --history ") == history {
					if pid, err := strconv.Atoi(fields[0]); err == nil {
						pids = append(pids, pid)
					}
				}
			}
		}
		return pids
	}
	listenerPIDs := func() []int { return processes(false) }
	historyPIDs := func() []int { return processes(true) }
	t.Cleanup(func() {
		for _, pid := range append(listenerPIDs(), historyPIDs()...) {
			t.Errorf("Vera process %d outlived the test; killing it", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken, cfg.DefaultProject = server.URL, "vera-api-canary", "66"
	cfg.DefaultWorkDir, cfg.TmuxSocket = "", socket
	cfg.DirectoryHistory = []string{repo, repo2}
	cfg.Providers["claude"] = Provider{Name: "Claude Code", Binary: provider}
	if err := SaveConfig(cfg, filepath.Join(root, "config.yaml")); err != nil {
		t.Fatal(err)
	}

	terminal := startReviewTUITerminal(t, binary, launchRepo, root, binDir)
	screen := func() string { return veraScreen(terminal.output.RawString(), 100, 30) }
	awaitScreen := func(want ...string) string {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for {
			visible, missing := screen(), ""
			for _, text := range want {
				if !strings.Contains(visible, text) {
					missing = text
					break
				}
			}
			if missing == "" {
				return visible
			}
			if time.Now().After(deadline) {
				t.Fatalf("screen never showed %q:\n%s", missing, visible)
			}
			select {
			case <-terminal.done:
				t.Fatalf("TUI exited before %q: %v\n%s", missing, terminal.err, visible)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	waitFor := func(what string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				hb, wp, del, reads := counts()
				t.Fatalf("missing %s: heartbeats=%d polls=%d deletes=%d repositoryReads=%d\n%s", what, hb, wp, del, reads, screen())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	lineWith := func(visible, text string) string {
		for _, line := range strings.Split(visible, "\n") {
			if strings.Contains(line, text) {
				return line
			}
		}
		return ""
	}
	quit := func() {
		t.Helper()
		terminal.send(t, "q")
		deadline := time.Now().Add(8 * time.Second)
		for exited := false; !exited; {
			select {
			case <-terminal.done:
				exited = true
			case <-time.After(20 * time.Millisecond):
				if strings.Contains(screen(), "Quit? (y/n)") {
					terminal.send(t, "y")
				}
				if time.Now().After(deadline) {
					t.Fatalf("TUI did not quit:\n%s", screen())
				}
			}
		}
		if terminal.err != nil {
			t.Fatalf("TUI quit: %v\n%s", terminal.err, screen())
		}
	}

	// 1. The TUI starts straight into the session list: no startup prompt, no
	// repository scan, no runner, and no R runners view.
	visible := awaitScreen("q: quit")
	if strings.Contains(visible, "Run PR reviews") {
		t.Fatalf("startup review prompt shown:\n%s", visible)
	}
	terminal.send(t, "R")
	time.Sleep(200 * time.Millisecond)
	if visible := screen(); strings.Contains(visible, "PR review runners") {
		t.Fatalf("R still opens a runners view:\n%s", visible)
	}
	if _, _, _, reads := counts(); reads != 0 || len(registered) != 0 {
		t.Fatalf("startup read %d repository lists or registered %v", reads, registered)
	}

	// 2. New Agent wizard: checkout, VibeFlow type, project, Vera only.
	terminal.send(t, "n")
	visible = awaitScreen("Select project directory:", repo, repo2)
	if !strings.HasPrefix(strings.TrimSpace(lineWith(visible, "Enter new path")), ">") {
		t.Fatalf("directory cursor not on first option:\n%s", visible)
	}
	terminal.send(t, "j\r")
	awaitScreen("Select session type:", "VibeFlow")
	terminal.send(t, "j\r")
	awaitScreen("Select a project:", "Axiom")
	terminal.send(t, "\r")
	visible = awaitScreen("Select team", "Vera · Code Reviewer")
	// awaitRow waits until the rendered row containing text satisfies ok, so
	// each key lands on the frame the previous key produced.
	awaitRow := func(what, text string, ok func(row string) bool) string {
		t.Helper()
		var visible string
		waitFor(what, func() bool { visible = screen(); return ok(lineWith(visible, text)) })
		return visible
	}
	cursorOn := func(row string) bool { return strings.HasPrefix(strings.TrimSpace(row), ">") }
	cursorRow := func(visible string) string {
		for _, line := range strings.Split(visible, "\n") {
			if cursorOn(line) {
				return line
			}
		}
		return ""
	}
	// Developer is preselected under the cursor; deselect it, then walk the
	// cursor down to Vera row by row and select Vera alone.
	if dev := lineWith(visible, "Developer"); !strings.Contains(dev, "> (●)") {
		t.Fatalf("Developer not preselected under the cursor:\n%s", visible)
	}
	terminal.send(t, " ")
	visible = awaitRow("Developer deselected", "Developer", func(row string) bool { return strings.Contains(row, "> ( )") })
	for !cursorOn(lineWith(visible, "Vera · Code Reviewer")) {
		before := cursorRow(visible)
		terminal.send(t, "j")
		waitFor("team cursor to move off "+before, func() bool { visible = screen(); return cursorRow(visible) != before })
	}
	terminal.send(t, " ")
	visible = awaitRow("Vera selected under the cursor", "Vera · Code Reviewer", func(row string) bool { return strings.Contains(row, "> [x]") })
	if strings.Count(visible, "[x]") != 1 || strings.Contains(visible, "(●)") {
		t.Fatalf("want Vera as the only team member:\n%s", visible)
	}
	terminal.send(t, "\r")

	// 3. The wizard's own Provider and Confirm steps.
	visible = awaitScreen("Select a provider:", "[Provider]", "Confirm", "Claude Code")
	for _, skipped := range []string{"Routing", "Branch", "Worktree", "Permissions"} {
		if strings.Contains(visible, skipped) {
			t.Fatalf("Vera provider step shows skipped step %q:\n%s", skipped, visible)
		}
	}
	terminal.send(t, "\r")
	visible = awaitScreen("Confirm Vera · Code Reviewer", "Project:   Axiom", "Checkout:", repo, "Harness:   Claude Code", "Model:     harness default", "own tmux session", "enter: start")
	t.Logf("confirm step for Vera:\n%s", visible)
	terminal.send(t, "\r")

	// 4. Vera is an ordinary session in the list, listening, named for its
	// repository; past PR reviews live in its history pane, not the list.
	visible = awaitScreen("◆ ● Vera · Code Reviewer", "listening")
	t.Logf("session list with Vera:\n%s", visible)
	time.Sleep(300 * time.Millisecond) // A review read would have landed by now.
	if visible := screen(); strings.Contains(visible, "PR #") || strings.Contains(visible, "late payment") || strings.Contains(visible, "Other repository") {
		t.Fatalf("PR reviews listed in the session list:\n%s", visible)
	}
	sessions := strings.Fields(tmux("list-sessions", "-F", "#{session_name}"))
	if len(sessions) != 1 || !strings.HasPrefix(sessions[0], sessionPrefix+"claude-") {
		t.Fatalf("want one Vera tmux session, got %v", sessions)
	}
	session := sessions[0]
	waitFor("two idle /work polls", func() bool { _, wp, _, _ := counts(); return wp >= 2 })
	mu.Lock()
	enrolled, sent := fmt.Sprint(registered), capabilities[7]
	mu.Unlock()
	if enrolled != "map[7:1]" {
		t.Fatalf("want exactly one registration, for link 7; got %s", enrolled)
	}
	if !reflect.DeepEqual(sent, []any{"repository_review_v1"}) {
		t.Fatalf("registration capabilities = %#v; want [repository_review_v1]", sent)
	}
	if data, err := os.ReadFile(modelRun); err == nil {
		t.Fatalf("idle listener started a model process: %s", data)
	}
	if _, err := os.Stat(filepath.Join(repo, ".vibeflow-session-code_reviewer")); !os.IsNotExist(err) {
		t.Fatal("Vera wrote ordinary coding session state")
	}
	// Two panes: the listener on the left, the history list on the right.
	panes := strings.Split(tmux("list-panes", "-t", session, "-F", "#{pane_id}\t#{pane_left}\t#{@vibeflow_vera_history}\t#{pane_start_command}"), "\n")
	if len(panes) != 2 {
		t.Fatalf("want two Vera panes, got %q", panes)
	}
	left, right := strings.SplitN(panes[0], "\t", 4), strings.SplitN(panes[1], "\t", 4)
	if left[1] != "0" || left[2] != "" || !strings.Contains(left[3], " review-watch --project 66 ") || right[2] != "1" || !strings.Contains(right[3], " review-watch --history --project 66 ") || !strings.Contains(right[3], " --repository-link 7 ") {
		t.Fatalf("Vera panes:\n%s", strings.Join(panes, "\n"))
	}
	pane := tmux("capture-pane", "-p", "-J", "-t", left[0]) // -J: the 70% pane wraps long lines.
	t.Logf("Vera listener pane:\n%s", pane)
	if !strings.Contains(pane, reviewListeningLine) || strings.Contains(pane, "vera-api-canary") {
		t.Fatalf("Vera pane:\n%s", pane)
	}
	var history string
	waitFor("history pane rows", func() bool {
		history = tmux("capture-pane", "-p", "-t", right[0])
		return strings.Contains(history, "#4 Add late") && strings.Contains(history, "Clean · round 2")
	})
	t.Logf("Vera history pane:\n%s", history)
	if strings.Contains(history, "#34") || strings.Contains(history, "vera-api-canary") {
		t.Fatalf("history pane shows another repository or the token:\n%s", history)
	}
	// Keys typed in the listener pane browse the history pane instead of
	// echoing; Enter opens the review, here inside the pane because no tmux
	// client is attached for a popup.
	if mouse := tmux("show-options", "-v", "-t", session, "mouse"); mouse != "on" {
		t.Fatalf("Vera session mouse = %q; want on", mouse)
	}
	selected := func(text string) func() bool {
		return func() bool {
			for _, line := range strings.Split(tmux("capture-pane", "-p", "-t", right[0]), "\n") {
				if strings.Contains(line, text) {
					return strings.HasPrefix(line, ">")
				}
			}
			return false
		}
	}
	waitFor("newest review selected", selected("#5 Fix rounding"))
	tmux("send-keys", "-t", left[0], "Down")
	waitFor("Down in the listener pane selects the next review", selected("#4 Add late"))
	tmux("send-keys", "-t", left[0], "Enter")
	waitFor("review detail", func() bool {
		history = tmux("capture-pane", "-p", "-t", right[0])
		return strings.Contains(history, "PR #4 · Add late") && strings.Contains(history, "Rounding drops") && strings.Contains(history, "billing/fee.go:42") && strings.Contains(history, "q/Esc close")
	})
	t.Logf("review detail in the history pane:\n%s", history)
	if pane := tmux("capture-pane", "-p", "-J", "-t", left[0]); strings.Contains(pane, "^[") || !strings.Contains(pane, veraBrowseHint) {
		t.Fatalf("listener pane echoed keys or lacks the hint:\n%s", pane)
	}
	tmux("send-keys", "-t", left[0], "Escape")
	waitFor("back to the review list", func() bool {
		return strings.Contains(tmux("capture-pane", "-p", "-t", right[0]), "Reviews · ")
	})
	if pids := listenerPIDs(); len(pids) != 1 {
		t.Fatalf("want one listener process, got %v", pids)
	}
	if pids := historyPIDs(); len(pids) != 1 {
		t.Fatalf("want one history process, got %v", pids)
	}

	// 5. Quitting the TUI leaves Vera listening, like any other persona.
	quit()
	_, before, _, _ := counts()
	waitFor("idle polling after TUI exit", func() bool { _, wp, _, _ := counts(); return wp > before })
	if _, _, del, _ := counts(); del != 0 || len(listenerPIDs()) != 1 {
		t.Fatalf("TUI exit stopped Vera: deletes=%d listeners=%v", del, listenerPIDs())
	}

	// 6. A new TUI lists the session; d deletes it, which deregisters and stops
	// the listener.
	terminal = startReviewTUITerminal(t, binary, launchRepo, root, binDir)
	awaitScreen("◆ ● Vera · Code Reviewer", "listening")
	terminal.send(t, "d")
	awaitScreen("y/n")
	terminal.send(t, "y")
	waitFor("runner DELETE", func() bool { _, _, del, _ := counts(); return del >= 1 })
	waitFor("listener and history exit", func() bool { return len(listenerPIDs()) == 0 && len(historyPIDs()) == 0 })
	mu.Lock()
	stopped := append([]string(nil), deleted...)
	stoppedLink := runnerLinks[stopped[0]]
	mu.Unlock()
	if len(stopped) != 1 || stoppedLink != 7 {
		t.Fatalf("want one DELETE for the link 7 runner, got %v", stopped)
	}
	hb, wp, _, _ := counts()
	// Negative check: nothing may heartbeat for a full poll interval after delete.
	time.Sleep(5500 * time.Millisecond)
	if afterHB, afterWP, _, _ := counts(); afterHB != hb || afterWP != wp {
		t.Fatalf("listener kept polling after delete: heartbeats %d->%d polls %d->%d", hb, afterHB, wp, afterWP)
	}
	quit()
}

// veraScreen replays a PTY transcript onto a width x height grid. Unlike
// reviewVisibleScreen it honors the hard tabs, erase modes, and line/char
// insert-delete that Bubble Tea v2's diff renderer emits, so leftover cells
// from earlier frames are not mistaken for product rendering bugs.
func veraScreen(raw string, width, height int) string {
	blank := func() []string {
		row := make([]string, width)
		for i := range row {
			row[i] = " "
		}
		return row
	}
	grid := make([][]string, height)
	for i := range grid {
		grid[i] = blank()
	}
	x, y, savedX, savedY, last := 0, 0, 0, 0, " "
	top, bottom := 0, height-1 // DECSTBM scroll region, inclusive.
	clamp := func() { x, y = min(max(x, 0), width-1), min(max(y, 0), height-1) }
	// scroll shifts rows from..bottom up (n > 0) or down (n < 0), blanking
	// the rows it exposes.
	scroll := func(from, n int) {
		rows := grid[from : bottom+1]
		if n > 0 {
			n = min(n, len(rows))
			copy(rows, rows[n:])
			for k := len(rows) - n; k < len(rows); k++ {
				rows[k] = blank()
			}
		} else {
			n = min(-n, len(rows))
			copy(rows[n:], rows[:len(rows)-n])
			for k := 0; k < n; k++ {
				rows[k] = blank()
			}
		}
	}
	lineFeed := func() {
		if y == bottom {
			scroll(top, 1)
		} else if y < height-1 {
			y++
		}
	}
	eraseRow := func(row, from, to int) {
		for col := max(0, from); col < min(width, to); col++ {
			grid[row][col] = " "
		}
	}
	for i := 0; i < len(raw); {
		switch c := raw[i]; {
		case c == '\x1b' && i+1 < len(raw) && raw[i+1] == '[':
			start := i + 2
			for i = start; i < len(raw) && (raw[i] < '@' || raw[i] > '~'); i++ {
			}
			if i >= len(raw) {
				i = len(raw)
				continue
			}
			final, params := raw[i], raw[start:i]
			i++
			if params != "" && strings.ContainsRune("?<=>", rune(params[0])) {
				if (params == "?1049" || params == "?1047" || params == "?47") && (final == 'h' || final == 'l') {
					for row := range grid {
						grid[row] = blank()
					}
					x, y = 0, 0
				}
				continue
			}
			fields := strings.Split(params, ";")
			arg := func(index, fallback int) int {
				if index < len(fields) {
					if v, err := strconv.Atoi(fields[index]); err == nil && v > 0 {
						return v
					}
				}
				return fallback
			}
			mode, _ := strconv.Atoi(fields[0])
			switch final {
			case 'H', 'f':
				y, x = arg(0, 1)-1, arg(1, 1)-1
			case 'A':
				y -= arg(0, 1)
			case 'B':
				y += arg(0, 1)
			case 'C':
				x += arg(0, 1)
			case 'D':
				x -= arg(0, 1)
			case 'E':
				y, x = y+arg(0, 1), 0
			case 'F':
				y, x = y-arg(0, 1), 0
			case 'G', '`':
				x = arg(0, 1) - 1
			case 'd':
				y = arg(0, 1) - 1
			case 'r':
				top, bottom = arg(0, 1)-1, min(arg(1, height), height)-1
				if top >= bottom {
					top, bottom = 0, height-1
				}
				x, y = 0, 0
			case 'J':
				clamp()
				switch mode {
				case 0:
					eraseRow(y, x, width)
					for row := y + 1; row < height; row++ {
						grid[row] = blank()
					}
				case 1:
					eraseRow(y, 0, x+1)
					for row := 0; row < y; row++ {
						grid[row] = blank()
					}
				default:
					for row := range grid {
						grid[row] = blank()
					}
				}
			case 'K':
				clamp()
				switch mode {
				case 0:
					eraseRow(y, x, width)
				case 1:
					eraseRow(y, 0, x+1)
				default:
					eraseRow(y, 0, width)
				}
			case 'X':
				clamp()
				eraseRow(y, x, x+arg(0, 1))
			case 'P', '@':
				clamp()
				n := min(arg(0, 1), width-x)
				row := grid[y]
				if final == 'P' {
					copy(row[x:], row[x+n:])
					eraseRow(y, width-n, width)
				} else {
					copy(row[x+n:], row[x:width-n])
					eraseRow(y, x, x+n)
				}
			case 'L', 'M', 'S', 'T':
				clamp()
				from, n := y, arg(0, 1)
				if final == 'S' || final == 'T' {
					from = top
				} else if y < top || y > bottom {
					break // Insert/delete line is a no-op outside the region.
				}
				if final == 'L' || final == 'T' {
					n = -n
				}
				scroll(from, n)
			case 'b':
				for k := 0; k < arg(0, 1); k++ {
					if x >= width {
						x = 0
						lineFeed()
					}
					grid[y][x] = last
					x++
				}
				continue
			}
			clamp()
		case c == '\x1b' && i+1 < len(raw):
			switch raw[i+1] {
			case ']', 'P', '_', '^': // OSC/DCS/APC/PM run to BEL or ST.
				for i += 2; i < len(raw) && raw[i] != '\a' && !(raw[i] == '\x1b' && i+1 < len(raw) && raw[i+1] == '\\'); i++ {
				}
				if i < len(raw) && raw[i] == '\x1b' {
					i++
				}
				i++
			case '(', ')':
				i += 3
			case '7':
				savedX, savedY, i = x, y, i+2
			case '8':
				x, y, i = savedX, savedY, i+2
			case 'M':
				if y == top {
					scroll(top, -1)
				} else if y > 0 {
					y--
				}
				i += 2
			default:
				i += 2
			}
		case c == '\r':
			x, i = 0, i+1
		case c == '\n':
			lineFeed()
			i++
		case c == '\t':
			x, i = min(width-1, (x/8+1)*8), i+1
		case c == '\b':
			x, i = max(0, x-1), i+1
		case c < 32 || c == 0x7f:
			i++
		default:
			r, size := utf8.DecodeRuneInString(raw[i:])
			i += size
			w := ansi.StringWidth(string(r))
			if w == 0 {
				continue
			}
			if x+w > width {
				x = 0
				lineFeed()
			}
			grid[y][x], last = string(r), string(r)
			if w == 2 {
				grid[y][x+1] = ""
			}
			x += w
		}
	}
	lines := make([]string, height)
	for row := range grid {
		lines[row] = strings.TrimRight(strings.Join(grid[row], ""), " ")
	}
	return strings.Join(lines, "\n")
}

// veraE2ESummary is a clean review of PR number on link; a higher number is a
// newer review.
func veraE2ESummary(job string, link, number int64, title string) string {
	head, base := strings.Repeat("c", 40), strings.Repeat("b", 40)
	at := 1790000000000 + number*1000
	return fmt.Sprintf(`{"review":{"id":%q,"project_id":66,"provider":"github","provider_host":"github.com","repository_link_id":%d,"number":%d,"head_sha":%q,"base_sha":%q,"state":"clean","rounds_started":2,"round_limit":3,"details":{"url":"https://github.com/acme/repo/pull/%d","base_repository_name":"acme/repo","title":%q}},"finding_count":1,"progress":{"reporting_version":1,"round_number":2,"attempt_number":1,"head_sha":%q,"base_sha":%q},"review_sessions":[{"session_id":"s-%s","project_id":66,"job_id":%q,"started_at":%d,"completed_at":%d}]}`, job, link, number, head, base, number, title, head, base, job, job, at, at+60000)
}
