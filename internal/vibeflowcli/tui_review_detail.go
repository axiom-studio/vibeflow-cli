package vibeflowcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type reviewProjectPage struct {
	Name, After, Next, Warning string
	Generation                 uint64
	Busy                       bool
	RequestedAfter             string
	Summaries                  []reviewSummary
}
type reviewBrowseRefreshMsg struct{}
type reviewProjectsMsg struct {
	generation uint64
	discovery  reviewDiscovery
	err        error
}
type reviewProjectRequest struct {
	Project    int64
	After      string
	Generation uint64
}
type reviewSummaryPageMsg struct {
	request reviewProjectRequest
	page    reviewSummariesPage
	err     error
}
type reviewSummaryPagesMsg []reviewSummaryPageMsg
type reviewDetailState struct {
	Project                                                int64
	Job                                                    string
	Generation                                             uint64
	Busy                                                   bool
	Summary                                                *reviewSummary
	Findings                                               []reviewFinding
	FindingsAfter, FindingsNext, HistoryAfter, HistoryNext string
	History                                                []reviewSession
	Warning, Notice, Diagnostic                            string
	Scroll                                                 int
}
type reviewDetailMsg struct {
	project    int64
	job        string
	generation uint64
	summary    reviewSummary
	findings   reviewFindingsPage
	history    reviewSessionsPage
	err        error
	diagnostic string
	resetPages bool
}
type reviewOpenMsg struct {
	project int64
	job     string
	err     error
}

func (m Model) craReviewSummaries() tea.Cmd {
	if !m.craEnabled {
		return nil
	}
	return func() tea.Msg { return reviewBrowseRefreshMsg{} }
}
func reviewAccessDenied(err error) bool {
	var response *reviewHTTPError
	return errors.As(err, &response) && (response.Status == 401 || response.Status == 403 || response.Status == 404)
}
func (m *Model) reviewReadCommands(ids []int64) tea.Cmd {
	if !m.craEnabled || m.client == nil {
		return nil
	}
	requests := make([]reviewProjectRequest, 0, len(ids))
	for _, id := range ids {
		p := m.reviewProjects[id]
		if p.Busy && p.RequestedAfter == p.After {
			continue
		}
		p.Generation++
		p.Busy, p.RequestedAfter = true, p.After
		m.reviewProjects[id] = p
		requests = append(requests, reviewProjectRequest{id, p.After, p.Generation})
	}
	if len(requests) == 0 {
		return nil
	}
	client := m.client
	if m.reviewReadSlots == nil {
		m.reviewReadSlots = make(chan struct{}, 4)
	}
	sem := m.reviewReadSlots
	return func() tea.Msg {
		results := make(reviewSummaryPagesMsg, len(requests))
		var wg sync.WaitGroup
		for i, request := range requests {
			sem <- struct{}{}
			wg.Add(1)
			go func(i int, request reviewProjectRequest) {
				defer wg.Done()
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				page, err := client.listReviewSummaries(ctx, request.Project, request.After)
				results[i] = reviewSummaryPageMsg{request, page, err}
			}(i, request)
		}
		wg.Wait()
		return results
	}
}
func (m *Model) rebuildReviewRows() {
	rows := make([]SessionRow, 0, len(m.sessions))
	for _, row := range m.sessions {
		if row.ManagedReview == nil {
			rows = append(rows, row)
		}
	}
	ids := make([]int64, 0, len(m.reviewProjects))
	for id := range m.reviewProjects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i] == m.projectID {
			return true
		}
		if ids[j] == m.projectID {
			return false
		}
		return ids[i] < ids[j]
	})
	warnings := []string{}
	for _, warning := range []string{m.reviewBrowseWarning, m.reviewBrowseError} {
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	for _, id := range ids {
		p := m.reviewProjects[id]
		for _, s := range p.Summaries {
			rows = append(rows, s.row())
		}
		if p.Warning != "" {
			warnings = append(warnings, fmt.Sprintf("Project %d: %s", id, p.Warning))
		}
	}
	m.reviewWarning = strings.Join(warnings, "; ")
	m.replaceSessionRows(rows)
}
func (m *Model) clearReviewProject(id int64) {
	p := m.reviewProjects[id]
	p.Generation++
	p.Busy = false
	p.Summaries = nil
	p.After = ""
	p.Next = ""
	p.Warning = "Reviews unavailable"
	m.reviewProjects[id] = p
	if m.reviewDetail.Project == id {
		m.reviewDetail.Generation++
		m.reviewDetail.Busy = false
		m.reviewDetail.Summary = nil
		m.reviewDetail.Findings = nil
		m.reviewDetail.History = nil
		m.reviewDetail.Diagnostic = ""
		m.reviewDetail.Warning = "Reviews unavailable"
	}
}
func (m Model) updateReviewReads(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case reviewBrowseRefreshMsg:
		if !m.craEnabled || m.client == nil || m.reviewProjectsBusy {
			return m, nil, true
		}
		m.reviewProjectGeneration++
		m.reviewProjectsBusy = true
		generation, client := m.reviewProjectGeneration, m.client
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			d, err := listReviewProjects(ctx, client)
			return reviewProjectsMsg{generation, d, err}
		}, true
	case reviewProjectsMsg:
		if !m.craEnabled || msg.generation != m.reviewProjectGeneration {
			return m, nil, true
		}
		m.reviewProjectsBusy = false
		if m.reviewProjects == nil {
			m.reviewProjects = map[int64]reviewProjectPage{}
		}
		if msg.err != nil {
			if reviewAccessDenied(msg.err) {
				for id := range m.reviewProjects {
					m.clearReviewProject(id)
				}
			}
			m.reviewBrowseError = "Review discovery unavailable; displayed reviews may be stale: " + reviewDisplay(msg.err.Error())
			m.rebuildReviewRows()
			return m, nil, true
		}
		m.reviewBrowseError = ""
		if msg.discovery.Complete || msg.discovery.Warning != "" {
			m.reviewBrowseWarning = reviewDisplay(msg.discovery.Warning)
		}
		present := map[int64]bool{}
		ids := []int64{}
		for _, project := range msg.discovery.Projects {
			present[project.ID] = true
			p := m.reviewProjects[project.ID]
			p.Name = project.Name
			m.reviewProjects[project.ID] = p
			ids = append(ids, project.ID)
		}
		if msg.discovery.Complete {
			for id := range m.reviewProjects {
				if !present[id] {
					m.clearReviewProject(id)
				}
			}
		}
		m.rebuildReviewRows()
		cmd := m.reviewReadCommands(ids)
		return m, cmd, true
	case reviewSummaryPagesMsg:
		if !m.craEnabled {
			return m, nil, true
		}
		for _, result := range msg {
			p, ok := m.reviewProjects[result.request.Project]
			if !ok || p.Generation != result.request.Generation || p.After != result.request.After {
				continue
			}
			p.Busy = false
			if result.err != nil {
				if reviewAccessDenied(result.err) {
					m.clearReviewProject(result.request.Project)
				} else {
					p.Warning = "Stale: " + reviewDisplay(result.err.Error())
					m.reviewProjects[result.request.Project] = p
				}
				continue
			}
			p.Summaries = result.page.Summaries
			p.Next = result.page.NextAfterID
			p.Warning = ""
			m.reviewProjects[result.request.Project] = p
		}
		m.rebuildReviewRows()
		return m, nil, true
	case reviewDetailMsg:
		d := &m.reviewDetail
		if !m.craEnabled || d.Project != msg.project || d.Job != msg.job || d.Generation != msg.generation {
			return m, nil, true
		}
		d.Busy = false
		if msg.err != nil {
			if reviewAccessDenied(msg.err) {
				m.clearReviewProject(msg.project)
				m.rebuildReviewRows()
			} else {
				d.Warning = "Stale: " + reviewDisplay(msg.err.Error())
			}
			return m, nil, true
		}
		d.Summary = &msg.summary
		d.Findings = msg.findings.Findings
		d.FindingsNext = msg.findings.NextAfterID
		d.History = msg.history.Sessions
		d.HistoryNext = msg.history.NextAfterID
		d.Warning = ""
		d.Diagnostic = msg.diagnostic
		if msg.resetPages {
			d.HistoryAfter = ""
			d.FindingsAfter = ""
			d.Scroll = 0
		}
		return m, nil, true
	case reviewOpenMsg:
		if m.craEnabled && m.reviewDetail.Project == msg.project && m.reviewDetail.Job == msg.job && msg.err != nil {
			m.reviewDetail.Notice = reviewDisplay(msg.err.Error())
		}
		return m, nil, true
	}
	return m, nil, false
}
func (m Model) pageReviewProject(older bool) (tea.Model, tea.Cmd) {
	if !m.craEnabled {
		return m, nil
	}
	s := m.selectedReview()
	if s == nil {
		return m, nil
	}
	p, ok := m.reviewProjects[s.ProjectID]
	if !ok {
		return m, nil
	}
	if older {
		if p.Next == "" {
			return m, nil
		}
		p.After = p.Next
	} else {
		p.After = ""
	}
	p.Next = ""
	p.Busy = false
	m.reviewProjects[s.ProjectID] = p
	cmd := m.reviewReadCommands([]int64{s.ProjectID})
	return m, cmd
}
func (m Model) activateSession(name string) (tea.Model, tea.Cmd) {
	s := m.managedReview(name)
	if s == nil {
		return m, m.attachSessionCmd(name)
	}
	if !m.craEnabled {
		return m, nil
	}
	generation := m.reviewDetail.Generation + 1
	m.reviewDetail = reviewDetailState{Project: s.ProjectID, Job: s.JobID, Generation: generation}
	for _, summary := range m.reviewProjects[s.ProjectID].Summaries {
		if summary.Review.ID == s.JobID {
			copy := summary
			m.reviewDetail.Summary = &copy
			break
		}
	}
	m.activeView = ViewReviewDetail
	cmd := m.requestReviewDetail()
	return m, cmd
}
func (m *Model) requestReviewDetail() tea.Cmd {
	if !m.craEnabled || m.client == nil || m.reviewDetail.Busy {
		return nil
	}
	m.reviewDetail.Generation++
	m.reviewDetail.Busy = true
	d := m.reviewDetail
	client := m.client
	supervisor := m.reviewSupervisor
	return func() tea.Msg {
		result := reviewDetailMsg{project: d.Project, job: d.Job, generation: d.Generation}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result.summary, result.err = client.getReviewSummary(ctx, d.Project, d.Job)
		if result.err != nil {
			return result
		}
		if d.Summary != nil && ((d.Summary.Progress == nil) != (result.summary.Progress == nil) || (d.Summary.Progress != nil && result.summary.Progress != nil && d.Summary.Progress.RoundID != result.summary.Progress.RoundID)) {
			d.HistoryAfter = ""
			d.FindingsAfter = ""
			result.resetPages = true
		}
		result.findings, result.err = client.listReviewSummaryFindings(ctx, d.Project, d.Job, d.FindingsAfter)
		if result.err != nil {
			return result
		}
		if d.HistoryAfter == "" {
			result.history = reviewSessionsPage{result.summary.ReviewSessions, result.summary.ReviewSessionsNext}
		} else {
			result.history, result.err = client.listReviewJobSessions(ctx, d.Project, d.Job, d.HistoryAfter)
		}
		result.diagnostic = readOwnedReviewDiagnostic(supervisor, result.summary)
		return result
	}
}

// Private receipt and attempt credentials stay on the command stack. Only the
// validated diagnostic's allowlisted display values cross back into the model.
func readOwnedReviewDiagnostic(s *reviewSupervisor, summary reviewSummary) string {
	if s == nil || summary.Progress == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.capacity == nil {
		return ""
	}
	for _, status := range s.statuses {
		o := status.Binding.Options
		if o.ProjectID != summary.Review.ProjectID || o.RepositoryLinkID != summary.Review.RepositoryLinkID || o.GitProvider != summary.Review.Provider {
			continue
		}
		owned := s.owned[status.BindingID]
		if owned == nil {
			continue
		}
		select {
		case <-owned.Done():
			continue
		default:
		}
		if status.BindingID != reviewBackgroundID(s.cfg.ServerURL, o) {
			continue
		}
		dir := filepath.Join(filepath.Dir(s.capacity.Directory), "review-runners", status.BindingID)
		file, err := os.Open(filepath.Join(dir, "state.json"))
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		file.Close()
		var state reviewRunnerState
		if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &state) != nil || state.OwnerID <= 0 || state.Pending == nil || state.Pending.Execution == nil {
			continue
		}
		r := state.Pending
		e := r.Execution
		if r.Capacity == nil || r.Capacity.Directory != s.capacity.Directory || r.JobID != summary.Review.ID || e.Review.ID != r.JobID || e.Attempt.RunnerID != state.ID || e.Attempt.Round.JobID != r.JobID || e.Attempt.Round.ID != summary.Progress.RoundID || e.Attempt.Round.HeadSHA != summary.Review.HeadSHA || e.Attempt.Round.BaseSHA != summary.Review.BaseSHA {
			continue
		}
		correlated := false
		for _, session := range summary.ReviewSessions {
			if session.RunnerID == state.ID && session.JobID == r.JobID && session.ProjectID == o.ProjectID && session.SessionID == e.Attempt.SessionID && session.RoundID == e.Attempt.Round.ID {
				correlated = true
				break
			}
		}
		if !correlated {
			continue
		}
		d, ok := readReviewExecutionDiagnostic(filepath.Join(dir, "last-provider-diagnostic.json"))
		if !ok || d.AttemptID != e.Attempt.ID {
			continue
		}
		code := "unknown"
		if d.ExitCode != nil {
			code = fmt.Sprint(*d.ExitCode)
		}
		return fmt.Sprintf("Local diagnostic: %s / %s / code %s / signal %d / %d ms", d.Category, d.Stage, code, d.Signal, d.DurationMS)
	}
	return ""
}
func reviewExternalCommand(goos, rawURL string) (*exec.Cmd, error) {
	if strings.IndexFunc(rawURL, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return nil, fmt.Errorf("unsafe review URL")
	}
	u, err := url.Parse(rawURL)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("unsafe review URL")
	}
	switch goos {
	case "darwin":
		return exec.Command("open", rawURL), nil
	case "linux":
		return exec.Command("xdg-open", rawURL), nil
	default:
		return nil, fmt.Errorf("Open this URL in your browser: %s", reviewDisplay(rawURL))
	}
}
func (m Model) updateReviewDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.activeView = ViewSessions
		m.reviewDetail.Generation++
		m.reviewDetail.Busy = false
		return m, nil
	case "down", "j":
		m.reviewDetail.Scroll++
	case "up", "k":
		if m.reviewDetail.Scroll > 0 {
			m.reviewDetail.Scroll--
		}
	case "pgdown":
		m.reviewDetail.Scroll += 10
	case "pgup":
		m.reviewDetail.Scroll = max(0, m.reviewDetail.Scroll-10)
	case "r":
		cmd := m.requestReviewDetail()
		return m, cmd
	case "]":
		if m.reviewDetail.HistoryNext != "" {
			m.reviewDetail.HistoryAfter = m.reviewDetail.HistoryNext
			m.reviewDetail.Busy = false
			cmd := m.requestReviewDetail()
			return m, cmd
		}
	case "[":
		m.reviewDetail.HistoryAfter = ""
		m.reviewDetail.Busy = false
		cmd := m.requestReviewDetail()
		return m, cmd
	case "n":
		if m.reviewDetail.FindingsNext != "" {
			m.reviewDetail.FindingsAfter = m.reviewDetail.FindingsNext
			m.reviewDetail.Busy = false
			cmd := m.requestReviewDetail()
			return m, cmd
		}
	case "p":
		m.reviewDetail.FindingsAfter = ""
		m.reviewDetail.Busy = false
		cmd := m.requestReviewDetail()
		return m, cmd
	case "o", "c":
		if m.reviewDetail.Summary == nil {
			return m, nil
		}
		raw := m.reviewDetail.Summary.Review.Details.URL
		if key.String() == "c" {
			if m.client == nil {
				return m, nil
			}
			u, err := url.Parse(m.client.baseURL)
			if err != nil {
				return m, nil
			}
			u.Path = "/ai/vibeflow"
			u.RawQuery = fmt.Sprintf("project=%d", m.reviewDetail.Project)
			u.Fragment = ""
			raw = u.String()
		}
		command, err := reviewExternalCommand(runtime.GOOS, raw)
		if err != nil {
			m.reviewDetail.Notice = err.Error()
			return m, nil
		}
		project, job := m.reviewDetail.Project, m.reviewDetail.Job
		return m, func() tea.Msg {
			err := command.Run()
			if err != nil {
				err = fmt.Errorf("Browser unavailable; open %s", reviewDisplay(raw))
			}
			return reviewOpenMsg{project, job, err}
		}
	}
	return m, nil
}
func (m Model) viewReviewDetail() string {
	d := m.reviewDetail
	lines := []string{reviewSessionLabel, fmt.Sprintf("Project: %s (%d)", reviewDisplay(m.reviewProjects[d.Project].Name), d.Project)}
	if d.Warning != "" {
		lines = append(lines, d.Warning)
	}
	if d.Notice != "" {
		lines = append(lines, d.Notice)
	}
	if s := d.Summary; s != nil {
		lines = append(lines, fmt.Sprintf("Repository: %s #%d", s.Review.Details.BaseRepositoryName, s.Review.Number), "Head: "+s.Review.HeadSHA, "Base: "+s.Review.BaseSHA, "Runner: "+s.Runner.Name+" "+s.Runner.State+" "+s.Runner.Reason)
		p := s.Progress
		if p != nil {
			lines = append(lines, fmt.Sprintf("Round %d / attempt %d: %s", p.RoundNumber, p.AttemptNumber, p.State))
			for _, stage := range []struct {
				name string
				done bool
			}{{"Request accepted", p.RequestAccepted}, {"Runner assigned", p.RunnerAssigned}, {"Checkout prepared", p.CheckoutPreparedAt > 0}, {"Code review completed", p.ReviewCompletedAt > 0 || p.ResultRecorded}, {"Result recorded", p.ResultRecorded}} {
				mark := " "
				if stage.done {
					mark = "x"
				}
				lines = append(lines, "["+mark+"] "+stage.name)
			}
			if !p.RunnerAssigned {
				lines = append(lines, "Waiting for runner")
			}
			if p.CheckoutPreparedAt == 0 {
				lines = append(lines, "Waiting for checkout confirmation")
			}
		} else {
			lines = append(lines, "Progress unavailable", "Waiting for runner / checkout")
		}
		lines = append(lines, "Summary: "+s.Summary, fmt.Sprintf("Findings: %d (%d blockers)", s.FindingCount, s.UnresolvedBlockers))
		for _, f := range d.Findings {
			lines = append(lines, fmt.Sprintf("%s %s: %s", f.Severity, f.State, f.Title), fmt.Sprintf("%s:%d", f.Path, f.Line), "Trigger: "+f.Trigger, "Impact: "+f.Impact, "Evidence: "+f.Evidence, "Verification: "+f.Verification)
		}
		if s.Publication != nil {
			lines = append(lines, "Publication: "+s.Publication.State, "Publication detail: "+s.Publication.LastError)
		}
		lines = append(lines, "Attempt history")
		for _, session := range d.History {
			lines = append(lines, session.progress()+" / "+session.RunnerName+" / "+reviewShortSHA(session.HeadSHA))
		}
	} else if d.Warning == "" {
		lines = append(lines, "Loading review...")
	}
	if d.Diagnostic != "" {
		lines = append(lines, d.Diagnostic)
	}
	lines = append(lines, "Review transcript unavailable")
	width := max(1, m.width)
	height := max(3, m.height)
	start := min(d.Scroll, max(0, len(lines)-(height-2)))
	end := min(len(lines), start+height-2)
	lines = lines[start:end]
	lines = append(lines, "o: PR  c: cloud  r: refresh  Esc: back", "[/]: history  n/p: findings  q: quit")
	for i, line := range lines {
		lines[i] = ansi.Truncate(reviewDisplay(line), width, "…")
	}
	return strings.Join(lines, "\n")
}
