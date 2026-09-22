package vibeflowcli

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewEnterOpensReadOnlyDetail(t *testing.T) {
	row := (reviewSession{ProjectID: 13, JobID: "job", SessionID: "session"}).row()
	m := Model{craEnabled: true, config: DefaultConfig(), sessions: []SessionRow{row}, repoRootCache: map[string]string{}, collapsedGroups: map[string]bool{}}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next.(Model).activeView != ViewReviewDetail {
		t.Fatal("review detail did not open")
	}
	if m.attachSessionCmd(row.Name) != nil {
		t.Fatal("review became attachable")
	}
}

func TestReviewSessionsPeriodicRefreshAcceptsPendingHTTP(t *testing.T) {
	for _, resource := range []string{"discovery", "summary", "detail"} {
		t.Run(resource, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/findings") {
					once.Do(func() { close(entered) })
					<-release
				}
				switch resource {
				case "discovery":
					json.NewEncoder(w).Encode(map[string]any{"projects": []Project{{ID: 13, Name: "Project"}}})
				case "summary":
					json.NewEncoder(w).Encode(reviewSummariesPage{Summaries: []reviewSummary{reviewTestSummary(13, "job")}})
				case "detail":
					if strings.HasSuffix(r.URL.Path, "/findings") {
						json.NewEncoder(w).Encode(reviewFindingsPage{})
					} else {
						json.NewEncoder(w).Encode(reviewTestSummary(13, "job"))
					}
				}
			}))
			defer server.Close()
			defer close(release)
			m := reviewTestModel(NewClient(server.URL, "fixture"))
			m.reviewProjects[13] = reviewProjectPage{}
			var cmd tea.Cmd
			switch resource {
			case "discovery":
				next, c := m.Update(reviewBrowseRefreshMsg{})
				m, cmd = next.(Model), c
			case "summary":
				cmd = m.reviewReadCommands([]int64{13})
			case "detail":
				m.activeView = ViewReviewDetail
				m.reviewDetail = reviewDetailState{Project: 13, Job: "job"}
				cmd = m.requestReviewDetail()
			}
			done := make(chan tea.Msg, 1)
			go func() { done <- cmd() }()
			<-entered
			// Deliver the regular timer and its browsing message without executing
			// unrelated local-session commands or waiting five wall-clock seconds.
			m = reviewApply(m, tickMsg(time.Now()))
			if resource == "discovery" {
				m = reviewApply(m, reviewBrowseRefreshMsg{})
			}
			if resource == "summary" {
				next, _ := m.Update(reviewProjectsMsg{generation: m.reviewProjectGeneration, discovery: reviewDiscovery{Projects: []Project{{ID: 13, Name: "Project"}}, Complete: true}})
				m = next.(Model)
			}
			// Release with a separate channel signal so deferred cleanup is safe.
			release <- struct{}{}
			m = reviewApply(m, <-done)
			switch resource {
			case "discovery":
				if m.reviewProjects[13].Name != "Project" || m.reviewProjectsBusy {
					t.Fatal("periodic refresh starved discovery")
				}
			case "summary":
				if len(m.sessions) != 1 || m.reviewProjects[13].Busy {
					t.Fatal("periodic refresh starved summary")
				}
			case "detail":
				if m.reviewDetail.Summary == nil || m.reviewDetail.Busy {
					t.Fatal("periodic refresh starved detail")
				}
			}
		})
	}
}

func TestReviewSessionsIncompleteEnumerationWarningSurvivesRows(t *testing.T) {
	m := reviewTestModel(NewClient("https://example.test", "fixture"))
	warning := "Legacy 200-project limit; discovery may be incomplete"
	m = reviewApply(m, reviewProjectsMsg{discovery: reviewDiscovery{Projects: []Project{{ID: 13, Name: "Project"}}, Warning: warning}})
	p := m.reviewProjects[13]
	m = reviewApply(m, reviewSummaryPagesMsg{{request: reviewProjectRequest{13, p.After, p.Generation}, page: reviewSummariesPage{Summaries: []reviewSummary{reviewTestSummary(13, "job")}}}})
	if !strings.Contains(m.reviewWarning, warning) {
		t.Fatal("healthy rows erased incomplete enumeration warning")
	}
	m = reviewApply(m, reviewProjectsMsg{discovery: reviewDiscovery{Projects: []Project{{ID: 13, Name: "Project"}}, Complete: true}})
	if strings.Contains(m.reviewWarning, warning) {
		t.Fatal("complete enumeration did not clear warning")
	}
}

func reviewTestSummary(project int64, job string) reviewSummary {
	s := reviewSummary{Review: reviewSummaryJob{reviewJob: reviewJob{ID: job, Provider: "github", ProviderHost: "github.com", RepositoryLinkID: 7, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), State: "queued"}, ProjectID: project, Number: 7}}
	s.Review.Details.URL = "https://github.com/acme/repo/pull/7"
	s.Review.Details.BaseRepositoryName = "acme/repo"
	s.Progress = &reviewProgress{HeadSHA: s.Review.HeadSHA, BaseSHA: s.Review.BaseSHA, State: "queued", RequestAccepted: true}
	return s
}
func reviewTestModel(client *Client) Model {
	return Model{craEnabled: true, client: client, config: DefaultConfig(), projectID: 14, width: 40, height: 40, repoRootCache: map[string]string{}, collapsedGroups: map[string]bool{}, reviewProjects: map[int64]reviewProjectPage{}}
}
func reviewApply(m Model, msg tea.Msg) Model { next, _ := m.Update(msg); return next.(Model) }

func TestReviewSessionNamesIncludeProject(t *testing.T) {
	a := (reviewSession{ProjectID: 13, SessionID: "same"}).row()
	b := (reviewSession{ProjectID: 14, SessionID: "same"}).row()
	if a.Name == b.Name {
		t.Fatal("cross-project row collision")
	}
	if reviewTestSummary(13, "same").row().Name == a.Name {
		t.Fatal("job/history collision")
	}
}
func TestReviewExternalCommandRejectsUnsafeLinks(t *testing.T) {
	for _, raw := range []string{"file:///tmp/a", "javascript:alert(1)", "https://user:secret@github.com/a/b", "https://github.com/a\n/b", "-rf", "https:///missing", "https://github.com/\u202econtrol"} {
		if _, err := reviewExternalCommand("darwin", raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	raw := "https://github.com/acme/repo/pull/7?x=$(touch%20ignored)"
	for _, tc := range []struct{ os, bin string }{{"darwin", "open"}, {"linux", "xdg-open"}} {
		cmd, err := reviewExternalCommand(tc.os, raw)
		if err != nil || len(cmd.Args) != 2 || cmd.Args[0] != tc.bin || cmd.Args[1] != raw {
			t.Fatalf("nonliteral command: %+v %v", cmd, err)
		}
	}
	if _, err := reviewExternalCommand("windows", raw); err == nil || !strings.Contains(err.Error(), raw) {
		t.Fatal("unsupported opener lacks actionable URL")
	}
}
func TestReviewSessionsBrowseWithoutRunnerConsent(t *testing.T) {
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != "GET" {
			t.Error("write from browser")
		}
		switch r.URL.Path {
		case "/rest/v1/vibeflow/projects":
			if r.URL.Query().Get("paginated") != "true" {
				t.Error("pagination missing")
			}
			json.NewEncoder(w).Encode(map[string]any{"projects": []Project{{ID: 13, Name: "First"}, {ID: 14, Name: "Default"}}})
		case "/rest/v1/vibeflow/projects/13/pr-review-summaries":
			json.NewEncoder(w).Encode(reviewSummariesPage{Summaries: []reviewSummary{reviewTestSummary(13, "job")}})
		case "/rest/v1/vibeflow/projects/14/pr-review-summaries":
			json.NewEncoder(w).Encode(reviewSummariesPage{Summaries: []reviewSummary{reviewTestSummary(14, "job")}})
		case "/rest/v1/vibeflow/projects/14/pr-review-summaries/job":
			json.NewEncoder(w).Encode(reviewTestSummary(14, "job"))
		case "/rest/v1/vibeflow/projects/14/pr-review-summaries/job/findings":
			json.NewEncoder(w).Encode(reviewFindingsPage{})
		default:
			t.Errorf("unexpected private or runner read %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	m := reviewTestModel(NewClient(server.URL, "fixture"))
	next, cmd := m.Update(m.craReviewSummaries()())
	m = next.(Model)
	next, cmd = m.Update(cmd())
	m = next.(Model)
	m = reviewApply(m, cmd())
	if len(m.sessions) != 2 || m.sessions[0].ManagedReview.ProjectID != 14 || m.reviewSupervisor != nil {
		t.Fatalf("consent-free cross-project rows missing: %+v", m.sessions)
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = reviewApply(next.(Model), cmd())
	if m.activeView != ViewReviewDetail {
		t.Fatal("queued job not selectable")
	}
	view := m.viewContent()
	for _, want := range []string{"Waiting for runner", "Waiting for checkout confirmation", "Review transcript unavailable", "[x] Request accepted"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
	if reads.Load() != 5 {
		t.Fatalf("unexpected requests %d", reads.Load())
	}
	m.craEnabled = false
	if m.craReviewSummaries() != nil {
		t.Fatal("no-flag command enabled")
	}
	next, cmd = m.Update(reviewBrowseRefreshMsg{})
	if cmd != nil {
		t.Fatal("no-flag reads enabled")
	}
	_, cmd = next.(Model).activateSession(m.sessions[0].Name)
	if cmd != nil {
		t.Fatal("no-flag activation enabled")
	}
}
func TestReviewSessionsDelayedCrossProjectPageCannotWin(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	otherEntered, otherRelease := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := int64(13)
		if strings.Contains(r.URL.Path, "/14/") {
			id = 14
			close(otherEntered)
			<-otherRelease
		} else if r.URL.Query().Get("after_id") == "" {
			close(entered)
			<-release
		}
		json.NewEncoder(w).Encode(reviewSummariesPage{Summaries: []reviewSummary{reviewTestSummary(id, "job-"+r.URL.Query().Get("after_id"))}})
	}))
	defer server.Close()
	m := reviewTestModel(NewClient(server.URL, "fixture"))
	m.reviewProjects[13] = reviewProjectPage{}
	m.reviewProjects[14] = reviewProjectPage{}
	old := m.reviewReadCommands([]int64{13})
	oldResult := make(chan tea.Msg, 1)
	go func() { oldResult <- old() }()
	<-entered
	p := m.reviewProjects[13]
	p.After = "older"
	m.reviewProjects[13] = p
	current := m.reviewReadCommands([]int64{13})
	m = reviewApply(m, current())
	other := m.reviewReadCommands([]int64{14})
	otherResult := make(chan tea.Msg, 1)
	go func() { otherResult <- other() }()
	<-otherEntered
	p = m.reviewProjects[13]
	m = reviewApply(m, reviewSummaryPagesMsg{{request: reviewProjectRequest{13, p.After, p.Generation}, err: &reviewHTTPError{Status: 403}}})
	close(release)
	m = reviewApply(m, <-oldResult)
	close(otherRelease)
	m = reviewApply(m, <-otherResult)
	if len(m.sessions) != 1 || m.sessions[0].ManagedReview.ProjectID != 14 {
		t.Fatalf("revoked project resurrected or other project erased: %+v", m.sessions)
	}
	p = m.reviewProjects[14]
	m = reviewApply(m, reviewSummaryPagesMsg{{request: reviewProjectRequest{14, p.After, p.Generation}, err: &reviewHTTPError{Status: 503}}})
	if len(m.sessions) != 1 || !strings.Contains(m.reviewWarning, "Stale") {
		t.Fatal("transient failure erased good project")
	}
}
func TestReviewSessionsSummaryRejectsMalformedPages(t *testing.T) {
	valid := reviewTestSummary(13, "job")
	foreign := reviewTestSummary(14, "job")
	badID := reviewTestSummary(13, "../execution")
	for _, page := range []reviewSummariesPage{{Summaries: []reviewSummary{foreign}}, {Summaries: []reviewSummary{badID}}, {Summaries: []reviewSummary{valid, valid}}, {Summaries: make([]reviewSummary, 26)}, {NextAfterID: "cursor"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(page) }))
		_, err := NewClient(server.URL, "fixture").listReviewSummaries(context.Background(), 13, "cursor")
		server.Close()
		if err == nil {
			t.Fatalf("invalid page accepted %+v", page)
		}
	}
	for _, page := range []reviewFindingsPage{{Findings: []reviewFinding{{ID: "finding", JobID: "other"}}}, {Findings: make([]reviewFinding, 26)}, {NextAfterID: "cursor"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(page) }))
		_, err := NewClient(server.URL, "fixture").listReviewSummaryFindings(context.Background(), 13, "job", "cursor")
		server.Close()
		if err == nil {
			t.Fatal("invalid findings accepted")
		}
	}
}
func TestReviewEnterMouseParityAndManagedControls(t *testing.T) {
	m := reviewTestModel(nil)
	s := reviewTestSummary(13, "job")
	m.sessions = []SessionRow{{Name: "ordinary"}, s.row()}
	m.hitmap = &listHitmap{leftWidth: 20, spans: []listRowSpan{{startY: 1, height: 2, pos: 1}}}
	next, cmd := m.handleListClick(2, 1)
	m = next.(Model)
	if cmd != nil || m.activeView != ViewSessions || m.cursor != 1 {
		t.Fatal("first click did not preview")
	}
	for _, key := range []string{"d", "b", "e", "m"} {
		next, cmd = m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		if cmd != nil || next.(Model).activeView != ViewSessions || next.(Model).confirmDelete {
			t.Fatalf("unsafe %s", key)
		}
	}
	next, _ = m.handleListClick(2, 1)
	clicked := next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if clicked.activeView != ViewReviewDetail || next.(Model).activeView != ViewReviewDetail {
		t.Fatal("mouse/keyboard mismatch")
	}
	next, _ = clicked.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if next.(Model).cursor != 1 || next.(Model).activeView != ViewSessions {
		t.Fatal("escape lost selection")
	}
	clicked.sessions = []SessionRow{s.row()}
	clicked.cursor = 0
	next, cmd = clicked.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if !next.(Model).quitting || cmd == nil {
		t.Fatal("detail quit failed")
	}
	if _, ok := m.storeMetaForRow(s.row()); ok || m.attachSessionCmd(s.row().Name) != nil {
		t.Fatal("managed summary acquired ordinary control")
	}
	m.killSessionByName(s.row().Name)
	if capture := m.refreshCapture().(captureMsg); capture.name != "" {
		t.Fatal("review capture touched tmux")
	}
}
func TestReviewSessionsDetailSanitizedAndRoundReset(t *testing.T) {
	m := reviewTestModel(nil)
	s := reviewTestSummary(13, "job")
	s.Summary = "unsafe\x1b]52;c;CANARY\a\n" + strings.Repeat("long", 200)
	s.Progress.RoundID = "round-old"
	s.Progress.ResultRecorded = true
	m.reviewDetail = reviewDetailState{Project: 13, Job: "job", Generation: 2, Summary: &s}
	m.activeView = ViewReviewDetail
	view := m.viewContent()
	if !strings.Contains(view, "[x] Code review completed") || !strings.Contains(view, "[ ] Checkout prepared") {
		t.Fatal("accepted result/checkout semantics incorrect")
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 40 || strings.ContainsAny(line, "\x1b\a") || strings.Contains(line, "CANARY") {
			t.Fatalf("unsafe 40-column output %q", line)
		}
	}
	newRound := reviewTestSummary(13, "job")
	newRound.Progress.RoundID = "round-new"
	m = reviewApply(m, reviewDetailMsg{project: 13, job: "job", generation: 2, summary: newRound})
	if strings.Contains(m.viewContent(), "[x] Code review completed") {
		t.Fatal("same-SHA new round retained completed projection")
	}
	m = reviewApply(m, reviewDetailMsg{project: 13, job: "job", generation: 1, summary: s})
	if m.reviewDetail.Summary.Progress.RoundID != "round-new" {
		t.Fatal("old detail won")
	}
	m = reviewApply(m, reviewDetailMsg{project: 13, job: "job", generation: 2, err: &reviewHTTPError{Status: 403}})
	if m.reviewDetail.Summary != nil || m.reviewDetail.Findings != nil {
		t.Fatal("denied detail retained")
	}
	m = reviewApply(m, reviewDetailMsg{project: 13, job: "job", generation: 2, summary: s})
	if m.reviewDetail.Summary != nil {
		t.Fatal("delayed detail resurrected denied scope")
	}
	if got := readOwnedReviewDiagnostic(nil, s); got != "" {
		t.Fatal("unowned diagnostic exposed")
	}
}

func TestReviewSessionsFanoutBounded(t *testing.T) {
	var active, peak atomic.Int64
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		<-release
		fmt.Fprint(w, `{"summaries":[]}`)
	}))
	defer server.Close()
	m := reviewTestModel(NewClient(server.URL, "fixture"))
	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	for _, id := range ids {
		m.reviewProjects[id] = reviewProjectPage{}
	}
	cmd := m.reviewReadCommands(ids)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	for i := 0; i < 4; i++ {
		<-entered
	}
	close(release)
	<-done
	if peak.Load() > 4 {
		t.Fatalf("fanout %d", peak.Load())
	}
}

func TestReviewSessionsLocalDiagnosticCorrelation(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.ServerURL = "https://example.test"
	o := reviewWatchOptions{ProjectID: 13, RepositoryLinkID: 7, GitProvider: "github", Kind: "local", Name: "fixture"}
	id := reviewBackgroundID(cfg.ServerURL, o)
	capacity := &reviewCapacity{Directory: filepath.Join(root, "review-capacity-fixture")}
	s := &reviewSupervisor{cfg: cfg, capacity: capacity, owned: map[string]*reviewOwnedRunner{id: {done: make(chan struct{})}}, statuses: []reviewRunnerStatus{{BindingID: id, Binding: reviewBinding{Options: o}, State: "online"}}}
	summary := reviewTestSummary(13, "job")
	summary.Progress.RoundID = "round"
	summary.ReviewSessions = []reviewSession{{ProjectID: 13, JobID: "job", RoundID: "round", SessionID: "session", RunnerID: "runner"}}
	e := &reviewExecution{Review: summary.Review.reviewJob}
	e.Attempt.ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	e.Attempt.RunnerID = "runner"
	e.Attempt.SessionID = "session"
	e.Attempt.Round.ID = "round"
	e.Attempt.Round.JobID = "job"
	e.Attempt.Round.HeadSHA = summary.Review.HeadSHA
	e.Attempt.Round.BaseSHA = summary.Review.BaseSHA
	state := reviewRunnerState{ID: "runner", OwnerID: 42, Pending: &reviewReceipt{JobID: "job", Execution: e, Capacity: &reviewReservation{Directory: capacity.Directory, Slot: "slot-0"}}}
	dir := filepath.Join(root, "review-runners", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	d := reviewExecutionDiagnostic{Version: 1, AttemptID: e.Attempt.ID, Provider: "claude", Stage: "provider", Category: "provider_exit", DurationMS: 15, RecordedAt: 1}
	write := func() {
		t.Helper()
		if err := saveReviewJSON(filepath.Join(dir, "state.json"), state); err != nil {
			t.Fatal(err)
		}
		if err := saveReviewJSON(filepath.Join(dir, "last-provider-diagnostic.json"), d); err != nil {
			t.Fatal(err)
		}
	}
	write()
	got := readOwnedReviewDiagnostic(s, summary)
	if !strings.Contains(got, "provider_exit") || strings.Contains(got, e.Attempt.ID) || strings.Contains(got, "runner") || strings.Contains(got, "session") {
		t.Fatalf("unsafe/missing diagnostic %q", got)
	}
	for _, field := range []*string{&state.ID, &state.Pending.JobID, &e.Attempt.RunnerID, &e.Attempt.SessionID, &e.Attempt.Round.ID, &e.Attempt.Round.JobID, &e.Attempt.ID, &state.Pending.Capacity.Directory} {
		old := *field
		*field = "mismatch"
		write()
		if readOwnedReviewDiagnostic(s, summary) != "" {
			t.Fatal("mismatched private correlation exposed")
		}
		*field = old
	}
	write()
	delete(s.owned, id)
	if readOwnedReviewDiagnostic(s, summary) != "" {
		t.Fatal("unowned diagnostic exposed")
	}
	s.owned[id] = &reviewOwnedRunner{done: make(chan struct{})}
	if err := os.WriteFile(filepath.Join(dir, "last-provider-diagnostic.json"), []byte(strings.Repeat(" ", 4097)), 0600); err != nil {
		t.Fatal(err)
	}
	if readOwnedReviewDiagnostic(s, summary) != "" {
		t.Fatal("oversized diagnostic exposed")
	}
	if err := os.Remove(filepath.Join(dir, "last-provider-diagnostic.json")); err != nil {
		t.Fatal(err)
	}
	if readOwnedReviewDiagnostic(s, summary) != "" {
		t.Fatal("missing diagnostic exposed")
	}
}
