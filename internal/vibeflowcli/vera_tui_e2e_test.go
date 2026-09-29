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

// Drives Vera from the New Agent picker in a real PTY after declining the
// all-project consent: exactly the chosen repository gets an idle, model-free
// runner, and quitting the TUI deregisters and stops it.
func TestVeraTUIBinaryPickerLifecycle(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("native script command unavailable; real PTY is required")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo, _ := reviewTestRepo(t) // origin acme/repo -> link 7
	repo2, _ := reviewTestRepo(t)
	reviewTestGit(t, repo2, "remote", "set-url", "origin", "https://github.com/acme/two.git") // link 8
	launchRepo, _ := reviewTestRepo(t)
	reviewTestGit(t, launchRepo, "remote", "set-url", "origin", "https://github.com/acme/cli.git")
	root, binDir, marks := t.TempDir(), t.TempDir(), t.TempDir()
	newSession, modelRun := filepath.Join(marks, "tmux-new-session"), filepath.Join(marks, "claude-model")
	tmuxScript := "#!/bin/sh\nfor arg in \"$@\"; do [ \"$arg\" = new-session ] && printf '%s\\n' \"$*\" >> " + shellQuote(newSession) + "; done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(tmuxScript), 0700); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(binDir, "claude")
	claudeScript := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(modelRun) + "\nexit 1\n"
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
			fmt.Fprint(w, `{"sessions":[]}`)
		case r.Method == "GET" && path == "/projects/66/pr-review-summaries":
			fmt.Fprint(w, `{"summaries":[]}`)
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

	// The owned runner lives in its own process group, so find it by its
	// unique --root rather than by the PTY's process tree.
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		roots = append(roots, resolved)
	}
	runnerPIDs := func() []int {
		out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "args=").Output()
		if err != nil {
			t.Fatalf("ps: %v", err)
		}
		var pids []int
		for _, line := range strings.Split(string(out), "\n") {
			for _, r := range roots {
				if strings.Contains(line, "--root "+r+" review-watch --owned-runner") {
					if pid, err := strconv.Atoi(strings.Fields(line)[0]); err == nil {
						pids = append(pids, pid)
					}
				}
			}
		}
		return pids
	}
	t.Cleanup(func() {
		for _, pid := range runnerPIDs() {
			t.Errorf("review runner %d outlived the test; killing it", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken, cfg.DefaultProject = server.URL, "vera-api-canary", "66"
	cfg.DefaultWorkDir, cfg.TmuxSocket = "", "vera-tui-test"
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
	// Every row of a popup must end on the same border column.
	checkBorder := func(name, visible string) {
		t.Helper()
		column := -1
		for _, line := range strings.Split(visible, "\n") {
			if i := strings.LastIndex(line, "│"); i >= 0 {
				if width := ansi.StringWidth(line[:i]); column < 0 {
					column = width
				} else if width != column {
					t.Errorf("%s popup border misaligned at %q (column %d, want %d)", name, strings.TrimSpace(line), width, column)
				}
			}
		}
	}

	// 1. Decline all-project automatic runners.
	awaitScreen("Run PR reviews while this CLI is open?")
	terminal.send(t, "\x1b")
	awaitScreen("q: quit")
	// Declined consent neither scans projects nor enrolls anything, and says how to add Vera.
	terminal.send(t, "R")
	awaitScreen("press n and choose Vera")
	t.Logf("runners view after declining consent:\n%s", screen())
	mu.Lock()
	enrolled := fmt.Sprint(registered)
	mu.Unlock()
	if _, _, _, reads := counts(); enrolled != "map[]" || reads != 0 {
		t.Fatalf("declined consent registered runners %s or read %d repository lists", enrolled, reads)
	}
	terminal.send(t, "R")
	awaitScreen("q: quit")

	// 2. New Agent wizard: checkout, VibeFlow type, project, Vera only.
	terminal.send(t, "n")
	visible := awaitScreen("Select project directory:", repo, repo2)
	t.Logf("directory step:\n%s", visible)
	if !strings.HasPrefix(strings.TrimSpace(lineWith(visible, "Enter new path")), ">") {
		t.Fatalf("directory cursor not on first option:\n%s", visible)
	}
	terminal.send(t, "j\r")
	awaitScreen("Select session type:", "VibeFlow")
	terminal.send(t, "j\r")
	visible = awaitScreen("Select a project:", "Axiom")
	t.Logf("project step:\n%s", visible)
	terminal.send(t, "\r")
	visible = awaitScreen("Select team", "Vera · Code Reviewer")
	t.Logf("team step with Vera listed:\n%s", visible)
	// Developer is preselected; deselect it, then select Vera (the last row) alone.
	terminal.send(t, " "+strings.Repeat("j", 20)+" ")
	visible = awaitScreen("[x]")
	if vera := lineWith(visible, "Vera · Code Reviewer"); !strings.Contains(vera, "> [x]") {
		t.Fatalf("Vera not selected under the cursor:\n%s", visible)
	}
	if developer := lineWith(visible, "Developer"); !strings.Contains(developer, "( )") {
		t.Fatalf("developer still selected alongside Vera:\n%s", visible)
	}
	t.Logf("team step with only Vera selected:\n%s", visible)
	terminal.send(t, "\r")

	// 3. Harness, then model.
	visible = awaitScreen("Vera · Code Reviewer", "Choose Vera", "Claude Code", "full permissions")
	t.Logf("harness choice:\n%s", visible)
	checkBorder("harness", visible)
	if !strings.Contains(visible, "> Claude Code") {
		t.Fatalf("Claude is not the default harness:\n%s", visible)
	}
	terminal.send(t, "\r")
	visible = awaitScreen("Choose the model for this Vera runner.", "Harness default")
	t.Logf("model choice:\n%s", visible)
	checkBorder("model", visible)
	if !strings.Contains(lineWith(visible, "Harness default"), "> Harness default") {
		t.Fatalf("model cursor not on the first choice:\n%s", visible)
	}
	terminal.send(t, "\r")

	// 4. Runners view: one online runner for the selected repository only.
	visible = awaitScreen("PR review runners", "Axiom / acme/repo [online]", "Listening for PR review requests")
	t.Logf("runners view:\n%s", visible)
	if strings.Contains(visible, "acme/two") {
		t.Fatalf("unselected repository appeared in runners view:\n%s", visible)
	}
	// Two idle polls prove the 5s loop keeps listening; the second costs one interval.
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
	if hb, _, _, _ := counts(); hb < 2 {
		t.Fatalf("runner heartbeats = %d; want at least one per poll", hb)
	}
	if data, err := os.ReadFile(modelRun); err == nil {
		t.Fatalf("idle runner started a model process: %s", data)
	}
	if data, err := os.ReadFile(newSession); err == nil {
		t.Fatalf("Vera created a coding tmux session: %s", data)
	}
	if _, err := os.Stat(filepath.Join(repo, ".vibeflow-session-code_reviewer")); !os.IsNotExist(err) {
		t.Fatal("Vera wrote ordinary coding session state")
	}
	if pids := runnerPIDs(); len(pids) != 1 {
		t.Fatalf("want one owned review-watch process, got %v", pids)
	}

	// 5. Quit: the owned runner deregisters and exits with the TUI.
	terminal.send(t, "R")
	t.Logf("sessions view with Vera running:\n%s", awaitScreen("q: quit"))
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
	waitFor("runner DELETE", func() bool { _, _, del, _ := counts(); return del >= 1 })
	waitFor("owned runner exit", func() bool { return len(runnerPIDs()) == 0 })
	mu.Lock()
	stopped := append([]string(nil), deleted...)
	stoppedLink := runnerLinks[stopped[0]]
	mu.Unlock()
	if len(stopped) != 1 || stoppedLink != 7 {
		t.Fatalf("want one DELETE for the link 7 runner, got %v", stopped)
	}
	hb, wp, _, _ := counts()
	// Negative check: nothing may heartbeat for a full poll interval after exit.
	time.Sleep(5500 * time.Millisecond)
	if afterHB, afterWP, _, _ := counts(); afterHB != hb || afterWP != wp {
		t.Fatalf("runner kept polling after TUI exit: heartbeats %d->%d polls %d->%d", hb, afterHB, wp, afterWP)
	}
	mu.Lock()
	enrolled = fmt.Sprint(registered)
	mu.Unlock()
	if enrolled != "map[7:1]" {
		t.Fatalf("unselected repository enrolled or runner re-registered: %s", enrolled)
	}
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
	clamp := func() { x, y = min(max(x, 0), width-1), min(max(y, 0), height-1) }
	lineFeed := func() {
		if y++; y >= height {
			grid, y = append(grid[1:], blank()), height-1
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
				top, n := y, arg(0, 1)
				if final == 'S' || final == 'T' {
					top = 0
				}
				n = min(n, height-top)
				rows := grid[top:]
				if final == 'M' || final == 'S' {
					copy(rows, rows[n:])
					for k := len(rows) - n; k < len(rows); k++ {
						rows[k] = blank()
					}
				} else {
					copy(rows[n:], rows[:len(rows)-n])
					for k := 0; k < n; k++ {
						rows[k] = blank()
					}
				}
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
				if y == 0 {
					grid = append([][]string{blank()}, grid[:height-1]...)
				} else {
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
