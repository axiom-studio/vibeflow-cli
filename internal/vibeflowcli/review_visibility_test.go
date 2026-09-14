package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestReviewListCommandWithoutOrdinarySessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing authorized read")
		}
		if r.URL.Path == "/rest/v1/vibeflow/projects/13/pr-review-sessions" {
			reads.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{{"session_id": "review-visible", "project_id": 13, "job_id": "job-visible", "pr_number": 57, "repository_name": "acme/repo", "provider": "github", "head_sha": strings.Repeat("a", 40), "state": "reviewing", "round_number": 2, "attempt_number": 1}}, "next_after_id": "review-visible"})
			return
		}
		t.Errorf("unexpected %s", r.URL)
		w.WriteHeader(404)
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = server.URL
	cfg.APIToken = "fixture-token"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--root", t.TempDir(), "--config", path, "--tmux-socket", fmt.Sprintf("review-list-%d", time.Now().UnixNano()), "list", "--project", "13")
	out, err := cmd.CombinedOutput()
	if err != nil || reads.Load() != 1 || !strings.Contains(string(out), "Principal Engineer · Review") || !strings.Contains(string(out), "acme/repo#57") || !strings.Contains(string(out), "--reviews-after review-visible") {
		t.Fatalf("managed review invisible: %v reads=%d\n%s", err, reads.Load(), out)
	}
	cfg.DefaultProject = "13"
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, binary, "--root", t.TempDir(), "--config", path, "--tmux-socket", fmt.Sprintf("review-list-%d", time.Now().UnixNano()), "list")
	out, err = cmd.CombinedOutput()
	if err != nil || reads.Load() != 2 || !strings.Contains(string(out), reviewSessionLabel) {
		t.Fatalf("configured project ignored: %v reads=%d\n%s", err, reads.Load(), out)
	}

}

func TestReviewRegistrationRequiresExactScopeEcho(t *testing.T) {
	previous := rootDir
	t.Cleanup(func() { rootDir = previous })
	for _, tc := range []struct {
		name, provider string
		link           int64
	}{{"missing", "", 0}, {"wrong_provider", "bitbucket", 7}, {"wrong_repository", "github", 8}} {
		t.Run(tc.name, func(t *testing.T) {
			SetRootDir(t.TempDir())
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var in map[string]any
				json.NewDecoder(r.Body).Decode(&in)
				if in["provider"] != "github" || in["repository_link_id"] != float64(7) {
					t.Error("wrong outbound scope")
				}
				json.NewEncoder(w).Encode(map[string]any{"id": in["id"], "user_id": 1, "provider": tc.provider, "repository_link_id": tc.link})
			}))
			defer server.Close()
			watch := &reviewWatch{client: NewClient(server.URL, "fixture-token"), cfg: DefaultConfig(), options: reviewWatchOptions{ProjectID: 13, RepositoryLinkID: 7, GitProvider: "github", Kind: "local", Name: "scope-test", Once: true}, output: io.Discard}
			err := watch.run(context.Background())
			if err == nil || !strings.Contains(err.Error(), "repository scope") || requests.Load() != 1 {
				t.Fatalf("unconfirmed scope continued: %v requests=%d", err, requests.Load())
			}
		})
	}
}

func TestReviewSessionsHTTPAndTUIReadOnlyHistory(t *testing.T) {
	var status atomic.Int64
	status.Store(200)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Path != "/rest/v1/vibeflow/projects/13/pr-review-sessions" || r.URL.Query().Get("limit") != "25" {
			t.Errorf("unsafe projection read: %s %s", r.Method, r.URL)
		}
		if status.Load() != 200 {
			w.WriteHeader(int(status.Load()))
			fmt.Fprint(w, "secret provider body")
			return
		}
		after := r.URL.Query().Get("after_id")
		state, id, next := "reviewing", "first", "first"
		if after != "" {
			if after != "first" {
				t.Error("incorrect cursor")
			}
			state, id, next = "completed", "older", ""
		}
		json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{{"session_id": id, "project_id": 13, "job_id": "job", "pr_number": 57, "repository_name": "acme/repo\x1b]52;c;secret\x07\n", "provider": "github", "head_sha": strings.Repeat("a", 40), "state": state, "round_number": 2, "attempt_number": 1, "attempt_id": "fencing-secret", "claim_token": "credential", "runner_name": "shared\x1b[2J", "runner_kind": "shared", "completed_at": int64(1)}}, "next_after_id": next})
	}))
	defer server.Close()
	m := Model{client: NewClient(server.URL, "fixture-token"), projectID: 13, config: DefaultConfig(), repoRootCache: map[string]string{}, collapsedGroups: map[string]bool{}, sessions: []SessionRow{{Name: "ordinary", WorkingDir: "/repo"}}}
	msg := m.refreshReviewSessions()
	next, _ := m.Update(msg)
	m = next.(Model)
	if len(m.sessions) != 2 || m.sessions[1].ManagedReview == nil || m.reviewNext != "first" {
		t.Fatalf("missing managed row: %+v", m)
	}
	m.cursor = 1
	detail := m.renderDetailPanel(100, 20)
	for _, want := range []string{reviewSessionLabel, "acme/repo", "reviewing", "round 2", "Read-only"} {
		if !strings.Contains(detail, want) {
			t.Errorf("missing %q: %s", want, detail)
		}
	}
	for _, bad := range []string{"\x1b", "\a", "fencing-secret", "credential", "Gateway", "Output", "resume", "chat", "brainstorm"} {
		if strings.Contains(detail, bad) {
			t.Errorf("unsafe detail %q: %s", bad, detail)
		}
	}
	m.width = 100
	m.height = 32
	view := m.viewContent()
	if !strings.Contains(view, "Read-only review") || strings.Contains(view, "d: delete") || strings.Contains(view, "m: project wb") {
		t.Errorf("managed row exposes ordinary controls: %s", view)
	}
	t.Log("Managed review view:\n" + stripANSI(view))
	// The real HTTP projection must never acquire ordinary interactive behavior.
	for _, key := range []string{"enter", "d", "b", "e", "m"} {
		next, cmd := m.Update(tea.KeyPressMsg{Code: []rune(key)[0], Text: key})
		got := next.(Model)
		if cmd != nil || got.confirmDelete || got.activeView != ViewSessions {
			t.Errorf("managed key %s activated ordinary action", key)
		}
	}
	if m.attachSessionCmd(m.sessions[1].Name) != nil {
		t.Error("managed row attached")
	}
	m.killSessionByName(m.sessions[1].Name) // nil tmux catches any attempted ordinary mutation.
	if _, ok := m.storeMetaForRow(m.sessions[1]); ok {
		t.Error("managed row acquired ordinary metadata")
	}
	if msg := m.refreshCapture().(captureMsg); msg.name != "" {
		t.Error("managed capture reached tmux")
	}
	if groups := m.projectGroups(); len(groups) != 1 || len(groups[0].Sessions) != 1 || groups[0].Sessions[0] != "ordinary" {
		t.Errorf("managed row entered workbench: %+v", groups)
	}
	if _, names := m.selectedProjectSessions(); len(names) != 0 {
		t.Errorf("managed row selected ordinary project: %v", names)
	}
	// Ordinary refresh retains the safe review selection and history.
	next, _ = m.Update(sessionsMsg{sessions: []SessionRow{{Name: "ordinary", WorkingDir: "/repo"}}})
	m = next.(Model)
	if m.selectedReview() == nil {
		t.Error("ordinary refresh lost managed selection")
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
	m = next.(Model)
	if cmd == nil || m.reviewAfter != "first" {
		t.Fatal("history page not requested")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.selectedReview() == nil || m.selectedReview().State != "completed" || m.reviewNext != "" {
		t.Fatal("completed history not retained")
	}
	status.Store(503)
	next, _ = m.Update(m.refreshReviewSessions())
	m = next.(Model)
	if len(m.sessions) != 2 || !strings.Contains(m.reviewWarning, "stale") || strings.Contains(m.reviewWarning, "secret") {
		t.Fatalf("outage erased history or leaked errors: %s", m.reviewWarning)
	}
	// A delayed old-page response must not replace the selected history page.
	next, _ = m.Update(msg)
	m = next.(Model)
	if m.selectedReview().SessionID != "older" {
		t.Error("stale page won")
	}
	status.Store(200)
	next, cmd = m.Update(tea.KeyPressMsg{Code: '[', Text: "["})
	m = next.(Model)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.reviewAfter != "" || m.selectedReview().SessionID != "first" || m.reviewWarning != "" {
		t.Error("latest page recovery failed")
	}
	m.groupMode = true
	m.buildGroups()
	m.cursor = 2 // review group header after ordinary header+row.
	if m.groupOrder[1] != reviewSessionsGroup {
		t.Errorf("unexpected managed group: %v", m.groupOrder)
	}
	if _, names := m.selectedProjectSessions(); len(names) != 0 {
		t.Error("managed group header offers a workbench")
	}
	if calls.Load() != 4 {
		t.Errorf("unbounded HTTP reads: %d", calls.Load())
	}
}

func TestReviewSessionsRejectsForeignAndUnboundedProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		page reviewSessionsPage
	}{
		{"foreign_project", reviewSessionsPage{Sessions: []reviewSession{{SessionID: "foreign", ProjectID: 14}}}},
		{"duplicate", reviewSessionsPage{Sessions: []reviewSession{{SessionID: "same", ProjectID: 13}, {SessionID: "same", ProjectID: 13}}}},
		{"cursor_cycle", reviewSessionsPage{NextAfterID: "cursor"}},
		{"oversized", reviewSessionsPage{Sessions: make([]reviewSession, 26)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(tc.page) }))
			defer server.Close()
			if _, err := NewClient(server.URL, "fixture-token").listReviewSessions(context.Background(), 13, "cursor"); err == nil {
				t.Fatal("invalid projection accepted")
			}
		})
	}
}

func TestReviewSessionsDelayedHTTPPageCannotWin(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after_id")
		if after == "" {
			close(entered)
			<-release
		}
		json.NewEncoder(w).Encode(reviewSessionsPage{Sessions: []reviewSession{{SessionID: "page-" + after, ProjectID: 13, State: "completed"}}})
	}))
	defer server.Close()
	m := Model{client: NewClient(server.URL, "fixture-token"), projectID: 13, config: DefaultConfig(), repoRootCache: map[string]string{}, collapsedGroups: map[string]bool{}}
	oldModel := m
	oldResult := make(chan tea.Msg, 1)
	go func() { oldResult <- oldModel.refreshReviewSessions() }()
	<-entered
	m.reviewAfter = "older"
	next, _ := m.Update(m.refreshReviewSessions())
	m = next.(Model)
	close(release)
	next, _ = m.Update(<-oldResult)
	m = next.(Model)
	if len(m.sessions) != 1 || m.sessions[0].ManagedReview.SessionID != "page-older" {
		t.Fatalf("delayed HTTP page replaced current selection: %+v", m.sessions)
	}
}

func TestReviewManagedQuitAndDetachConfirmations(t *testing.T) {
	for _, key := range []string{"q", "D"} {
		t.Run(key, func(t *testing.T) {
			m := Model{config: DefaultConfig(), width: 100, height: 32, repoRootCache: map[string]string{}, collapsedGroups: map[string]bool{}, sessions: []SessionRow{{Name: "ordinary", WorkingDir: "/repo"}, (reviewSession{SessionID: "review-history", ProjectID: 13, State: "completed"}).row()}, cursor: 1}
			next, _ := m.Update(tea.KeyPressMsg{Code: []rune(key)[0], Text: key})
			got := next.(Model)
			view := stripANSI(got.viewContent())
			if !strings.Contains(view, "(y/n)") || !strings.Contains(view, "1 local session") || strings.Contains(view, "2 session") || strings.Contains(view, "Read-only review  r:") {
				t.Fatalf("confirmation hidden or counts history: %s", view)
			}
			next, cmd := got.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
			if !next.(Model).quitting || cmd == nil {
				t.Fatal("visible confirmation did not quit")
			}
			m.sessions = m.sessions[1:]
			m.cursor = 0
			next, cmd = m.Update(tea.KeyPressMsg{Code: []rune(key)[0], Text: key})
			if !next.(Model).quitting || cmd == nil {
				t.Fatal("completed managed history blocks exit")
			}
		})
	}
}

func TestReviewManagedAuthorityFailureClearsHistory(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "private provider error")
			}))
			defer server.Close()
			m := Model{client: NewClient(server.URL, "fixture-token"), projectID: 13, config: DefaultConfig(), reviewAfter: "old-cursor", reviewNext: "next-cursor", repoRootCache: map[string]string{}, collapsedGroups: map[string]bool{}, sessions: []SessionRow{{Name: "ordinary"}, (reviewSession{SessionID: "review-history", ProjectID: 13, State: "completed", RepositoryName: "private-repo"}).row()}, cursor: 1}
			next, _ := m.Update(m.refreshReviewSessions())
			got := next.(Model)
			if len(got.sessions) != 1 || got.sessions[0].Name != "ordinary" || got.selectedReview() != nil || got.reviewAfter != "" || got.reviewNext != "" {
				t.Fatalf("authority failure retained review data: rows=%d selected=%v after=%q next=%q", len(got.sessions), got.selectedReview() != nil, got.reviewAfter, got.reviewNext)
			}
			if strings.Contains(got.reviewWarning, "stale") || strings.Contains(got.reviewWarning, "private") {
				t.Fatalf("incorrect authority warning: %s", got.reviewWarning)
			}
		})
	}
}

func TestReviewWatchIntervalBounds(t *testing.T) {
	previousRoot, previousConfig := rootDir, flagConfigPath
	t.Cleanup(func() { rootDir = previousRoot; flagConfigPath = previousConfig })
	SetRootDir(t.TempDir())
	flagConfigPath = filepath.Join(RootDir(), "config.yaml")
	for _, tc := range []struct {
		interval string
		ok       bool
	}{{"1s", true}, {"60s", true}, {"61s", false}, {"5m", false}} {
		t.Run(tc.interval, func(t *testing.T) {
			cmd := reviewWatchCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--project", "1", "--repository-link", "7", "--interval", tc.interval})
			err := cmd.ExecuteContext(context.Background())
			// Accepted intervals proceed to the later "connect VibeFlow" check.
			rejected := err != nil && strings.Contains(err.Error(), "interval must be between 1s and 60s")
			if rejected == tc.ok {
				t.Fatalf("interval %s ok=%v: %v", tc.interval, tc.ok, err)
			}
		})
	}
}

func TestReviewListCommandSurvivesReviewAPIFailure(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "private provider error", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, tc := range []struct{ name, url, want string }{
		{"http_503", server.URL, "managed reviews unavailable: review API returned HTTP 503"},
		{"unreachable", "http://127.0.0.1:1", "managed reviews unavailable: review API connection failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ServerURL = tc.url
			cfg.APIToken = "fixture-token"
			cfg.DefaultProject = "13"
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := SaveConfig(cfg, path); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--root", t.TempDir(), "--config", path, "--tmux-socket", fmt.Sprintf("review-list-%d", time.Now().UnixNano()), "list")
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if err != nil || !strings.Contains(stdout.String(), "sessions") || !strings.Contains(stderr.String(), tc.want) || strings.Contains(stderr.String(), "private") {
				t.Fatalf("review outage broke local listing: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
		})
	}
}

func TestReviewUnconfiguredProjectShowsHintNotStaleWarning(t *testing.T) {
	m := Model{client: NewClient("http://127.0.0.1:1", "fixture-token"), config: DefaultConfig(), width: 120, height: 32, serverWarning: "Server unreachable (http://127.0.0.1:1)", repoRootCache: map[string]string{}, collapsedGroups: map[string]bool{}, sessions: []SessionRow{{Name: "ordinary", WorkingDir: "/repo"}}}
	for i := 0; i < 2; i++ { // the refresh re-fires every tick
		next, _ := m.Update(m.refreshReviewSessions())
		m = next.(Model)
	}
	for _, sessions := range [][]SessionRow{m.sessions, nil} {
		m.sessions = sessions
		view := stripANSI(m.viewContent())
		if m.reviewWarning != "" || strings.Contains(view, "stale") || strings.Contains(view, "Managed reviews unavailable") {
			t.Fatalf("unconfigured project reported as outage: %q\n%s", m.reviewWarning, view)
		}
		if strings.Count(view, "no project selected") != 1 || !strings.Contains(view, "Server unreachable") {
			t.Fatalf("missing hint or server warning outranked:\n%s", view)
		}
	}
}
