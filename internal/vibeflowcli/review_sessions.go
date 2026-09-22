package vibeflowcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const reviewSessionLabel = "Principal Engineer · Review"
const reviewSessionsGroup = "PR reviews"

// Safe server projection. Attempt IDs, tokens and ordinary persona metadata
// deliberately have no place here. History remains owned by the backend.
type reviewSession struct {
	SessionID        string `json:"session_id"`
	ProjectID        int64  `json:"project_id"`
	JobID            string `json:"job_id"`
	RoundID          string `json:"round_id"`
	RoundNumber      int    `json:"round_number"`
	AttemptNumber    int    `json:"attempt_number"`
	Provider         string `json:"provider"`
	ProviderHost     string `json:"provider_host"`
	RepositoryLinkID int64  `json:"repository_link_id"`
	RepositoryName   string `json:"repository_name"`
	PRNumber         int64  `json:"pr_number"`
	PRURL            string `json:"pr_url"`
	HeadSHA          string `json:"head_sha"`
	BaseSHA          string `json:"base_sha"`
	GitBranch        string `json:"git_branch"`
	RunnerID         string `json:"runner_id"`
	RunnerKind       string `json:"runner_kind"`
	RunnerName       string `json:"runner_name"`
	State            string `json:"state"`
	Active           bool   `json:"active"`
	StartedAt        int64  `json:"started_at"`
	CompletedAt      int64  `json:"completed_at"`
	LastContactAt    int64  `json:"last_contact_at"`
}

type reviewSessionsPage struct {
	Sessions    []reviewSession `json:"sessions"`
	NextAfterID string          `json:"next_after_id"`
}

type reviewSummaryJob struct {
	reviewJob
	ProjectID int64 `json:"project_id"`
	Number    int64 `json:"number"`
	Details   struct {
		URL                string `json:"url"`
		BaseRepositoryName string `json:"base_repository_name"`
	} `json:"details"`
}
type reviewProgress struct {
	ReportingVersion   int    `json:"reporting_version"`
	RoundID            string `json:"round_id"`
	RoundNumber        int    `json:"round_number"`
	AttemptNumber      int    `json:"attempt_number"`
	HeadSHA            string `json:"head_sha"`
	BaseSHA            string `json:"base_sha"`
	RequestAccepted    bool   `json:"request_accepted"`
	RunnerAssigned     bool   `json:"runner_assigned"`
	CheckoutPreparedAt int64  `json:"checkout_prepared_at"`
	ReviewCompletedAt  int64  `json:"review_completed_at"`
	ResultRecorded     bool   `json:"result_recorded"`
	State              string `json:"state"`
}
type reviewSummary struct {
	Review             reviewSummaryJob `json:"review"`
	Summary            string           `json:"summary"`
	FindingCount       int              `json:"finding_count"`
	UnresolvedBlockers int              `json:"unresolved_blockers"`
	Progress           *reviewProgress  `json:"progress"`
	ReviewSessions     []reviewSession  `json:"review_sessions"`
	ReviewSessionsNext string           `json:"review_sessions_next_after_id"`
	Runner             struct {
		State  string `json:"state"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	} `json:"runner"`
	Publication *struct {
		State     string `json:"state"`
		LastError string `json:"last_error"`
	} `json:"publication"`
}
type reviewSummariesPage struct {
	Summaries   []reviewSummary `json:"summaries"`
	NextAfterID string          `json:"next_after_id"`
}
type reviewFinding struct {
	ID           string `json:"id"`
	JobID        string `json:"job_id"`
	Title        string `json:"title"`
	Severity     string `json:"severity"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	State        string `json:"state"`
	HeadSHA      string `json:"head_sha"`
	BaseSHA      string `json:"base_sha"`
	Trigger      string `json:"trigger"`
	Impact       string `json:"impact"`
	Evidence     string `json:"evidence"`
	Verification string `json:"verification"`
}
type reviewFindingsPage struct {
	Findings    []reviewFinding `json:"findings"`
	NextAfterID string          `json:"next_after_id"`
}

func reviewPublicID(s string) bool {
	if s == "" || len(s) > 160 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func validReviewSummary(s reviewSummary, project int64, job string) bool {
	if s.Review.ProjectID != project || !reviewPublicID(s.Review.ID) || (job != "" && s.Review.ID != job) || len(s.ReviewSessions) > 25 || (s.ReviewSessionsNext != "" && !reviewPublicID(s.ReviewSessionsNext)) {
		return false
	}
	seen := map[string]bool{}
	for _, v := range s.ReviewSessions {
		if v.ProjectID != project || v.JobID != s.Review.ID || !reviewPublicID(v.SessionID) || seen[v.SessionID] {
			return false
		}
		seen[v.SessionID] = true
	}
	if p := s.Progress; p != nil && (p.ReportingVersion < 0 || p.ReportingVersion > 1 || p.HeadSHA != s.Review.HeadSHA || p.BaseSHA != s.Review.BaseSHA || (p.RoundID != "" && !reviewPublicID(p.RoundID))) {
		return false
	}
	return true
}
func (c *Client) listReviewSummaries(ctx context.Context, projectID int64, after string) (reviewSummariesPage, error) {
	var page reviewSummariesPage
	if projectID <= 0 || (after != "" && !reviewPublicID(after)) {
		return page, fmt.Errorf("invalid review project or cursor")
	}
	q := url.Values{"limit": {"25"}, "after_id": {after}}
	if err := c.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-summaries?%s", projectID, q.Encode()), nil, &page); err != nil {
		return page, err
	}
	if len(page.Summaries) > 25 || (page.NextAfterID != "" && (!reviewPublicID(page.NextAfterID) || page.NextAfterID == after)) {
		return reviewSummariesPage{}, fmt.Errorf("invalid review summary page")
	}
	seen := map[string]bool{}
	for _, s := range page.Summaries {
		if !validReviewSummary(s, projectID, "") || seen[s.Review.ID] {
			return reviewSummariesPage{}, fmt.Errorf("invalid review summary scope")
		}
		seen[s.Review.ID] = true
	}
	return page, nil
}
func (c *Client) getReviewSummary(ctx context.Context, projectID int64, jobID string) (reviewSummary, error) {
	var s reviewSummary
	if projectID <= 0 || !reviewPublicID(jobID) {
		return s, fmt.Errorf("invalid review identity")
	}
	if err := c.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-summaries/%s", projectID, jobID), nil, &s); err != nil {
		return s, err
	}
	if !validReviewSummary(s, projectID, jobID) {
		return reviewSummary{}, fmt.Errorf("invalid review summary scope")
	}
	return s, nil
}
func (c *Client) listReviewSummaryFindings(ctx context.Context, projectID int64, jobID, after string) (reviewFindingsPage, error) {
	var page reviewFindingsPage
	if projectID <= 0 || !reviewPublicID(jobID) || (after != "" && !reviewPublicID(after)) {
		return page, fmt.Errorf("invalid finding identity")
	}
	q := url.Values{"limit": {"25"}, "after_id": {after}}
	if err := c.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-summaries/%s/findings?%s", projectID, jobID, q.Encode()), nil, &page); err != nil {
		return page, err
	}
	if len(page.Findings) > 25 || (page.NextAfterID != "" && (!reviewPublicID(page.NextAfterID) || page.NextAfterID == after)) {
		return reviewFindingsPage{}, fmt.Errorf("invalid finding page")
	}
	seen := map[string]bool{}
	for _, f := range page.Findings {
		if f.JobID != jobID || !reviewPublicID(f.ID) || seen[f.ID] {
			return reviewFindingsPage{}, fmt.Errorf("invalid finding scope")
		}
		seen[f.ID] = true
	}
	return page, nil
}
func (c *Client) listReviewJobSessions(ctx context.Context, projectID int64, jobID, after string) (reviewSessionsPage, error) {
	var page reviewSessionsPage
	if projectID <= 0 || !reviewPublicID(jobID) || (after != "" && !reviewPublicID(after)) {
		return page, fmt.Errorf("invalid history identity")
	}
	q := url.Values{"limit": {"25"}, "job_id": {jobID}, "after_id": {after}}
	if err := c.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-sessions?%s", projectID, q.Encode()), nil, &page); err != nil {
		return page, err
	}
	if len(page.Sessions) > 25 || (page.NextAfterID != "" && (!reviewPublicID(page.NextAfterID) || page.NextAfterID == after)) {
		return reviewSessionsPage{}, fmt.Errorf("invalid history page")
	}
	seen := map[string]bool{}
	for _, s := range page.Sessions {
		if s.ProjectID != projectID || s.JobID != jobID || !reviewPublicID(s.SessionID) || seen[s.SessionID] {
			return reviewSessionsPage{}, fmt.Errorf("invalid history scope")
		}
		seen[s.SessionID] = true
	}
	return page, nil
}
func (s reviewSummary) row() SessionRow {
	r := s.Review
	v := reviewSession{ProjectID: r.ProjectID, JobID: r.ID, Provider: r.Provider, ProviderHost: r.ProviderHost, RepositoryLinkID: r.RepositoryLinkID, RepositoryName: r.Details.BaseRepositoryName, PRNumber: r.Number, PRURL: r.Details.URL, HeadSHA: r.HeadSHA, BaseSHA: r.BaseSHA, State: r.State, RunnerName: s.Runner.Name}
	if p := s.Progress; p != nil {
		v.RoundID, v.RoundNumber, v.AttemptNumber = p.RoundID, p.RoundNumber, p.AttemptNumber
	}
	row := v.row()
	row.Name = fmt.Sprintf("review:%d:job:%s", r.ProjectID, r.ID)
	return row
}

func (c *Client) listReviewSessions(ctx context.Context, projectID int64, after string) (reviewSessionsPage, error) {
	var page reviewSessionsPage
	if projectID <= 0 || len(after) > 256 {
		return page, fmt.Errorf("select a valid project and review history cursor")
	}
	query := url.Values{"limit": {"25"}}
	if after != "" {
		query.Set("after_id", after)
	}
	err := c.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-sessions?%s", projectID, query.Encode()), nil, &page)
	if err != nil {
		return page, err
	}
	if len(page.Sessions) > 25 || len(page.NextAfterID) > 256 || (page.NextAfterID != "" && page.NextAfterID == after) {
		return reviewSessionsPage{}, fmt.Errorf("invalid review history page")
	}
	seen := map[string]bool{}
	for _, s := range page.Sessions {
		if s.SessionID == "" || len(s.SessionID) > 256 || s.ProjectID != projectID || seen[s.SessionID] {
			return reviewSessionsPage{}, fmt.Errorf("invalid review session scope")
		}
		seen[s.SessionID] = true
	}
	return page, nil
}

func reviewDisplay(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > 512 {
		s = string(r[:512]) + "..."
	}
	return s
}
func (s reviewSession) pullRequest() string {
	repo := s.RepositoryName
	if s.ProviderHost != "" {
		repo = s.ProviderHost + "/" + repo
	}
	return fmt.Sprintf("%s#%d", reviewDisplay(repo), s.PRNumber)
}
func (s reviewSession) progress() string {
	return fmt.Sprintf("%s · round %d · attempt %d", reviewDisplay(s.State), s.RoundNumber, s.AttemptNumber)
}
func (s reviewSession) row() SessionRow {
	return SessionRow{Name: fmt.Sprintf("review:%d:session:%s", s.ProjectID, s.SessionID), Persona: reviewSessionLabel, Project: s.pullRequest(), Status: reviewDisplay(s.State), ManagedReview: &s}
}
func (m Model) localSessionCount() int {
	n := 0
	for _, s := range m.sessions {
		if s.ManagedReview == nil {
			n++
		}
	}
	return n
}

func (m Model) managedReview(name string) *reviewSession {
	for _, s := range m.sessions {
		if s.Name == name {
			return s.ManagedReview
		}
	}
	return nil
}
func (m Model) reviewSelection() bool {
	if m.selectedReview() != nil {
		return true
	}
	if m.groupMode {
		idx, root := m.groupedCursorToSession()
		return idx < 0 && strings.HasPrefix(root, reviewSessionsGroup)
	}
	return false
}

func (m Model) selectedReview() *reviewSession {
	if idx := m.selectedSessionIdx(); idx >= 0 && idx < len(m.sessions) {
		return m.sessions[idx].ManagedReview
	}
	return nil
}

func printReviewSessions(ctx context.Context, out io.Writer, cfg *Config, project, after string) error {
	if project == "" {
		project = cfg.DefaultProject
	}
	if project == "" {
		fmt.Fprintln(out, "Managed reviews: use list --project <id-or-name> to show current reviews and history.")
		return nil
	}
	if cfg.APIToken == "" {
		return fmt.Errorf("configure authentication first")
	}
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	id, err := strconv.ParseInt(project, 10, 64)
	if err != nil {
		var projects []Project
		if err = client.reviewRequest(ctx, "GET", "/projects", nil, &projects); err != nil {
			return err
		}
		for _, p := range projects {
			if p.Name == project {
				id = p.ID
				break
			}
		}
	}
	if id <= 0 {
		return fmt.Errorf("project not found")
	}
	page, err := client.listReviewSessions(ctx, id, after)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\n"+reviewSessionLabel+" (read-only)")
	if len(page.Sessions) == 0 {
		fmt.Fprintln(out, "No managed reviews on this page.")
	}
	for _, s := range page.Sessions {
		fmt.Fprintf(out, "%s  %s  %s\n  head %s  base %s\n  runner %s (%s)  %s\n", s.pullRequest(), s.progress(), reviewDisplay(s.SessionID), reviewDisplay(s.HeadSHA), reviewDisplay(s.BaseSHA), reviewDisplay(s.RunnerName), reviewDisplay(s.RunnerKind), reviewDisplay(s.PRURL))
	}
	if page.NextAfterID != "" {
		fmt.Fprintf(out, "Older reviews: list --project %d --reviews-after %s\n", id, reviewDisplay(page.NextAfterID))
	}
	return nil
}

type reviewSessionsMsg struct {
	page    reviewSessionsPage
	after   string
	started time.Time
	err     error
}

// errReviewProjectUnconfigured means no project resolved for the TUI; it is a
// configuration state, not an outage, so it never produces a stale warning.
var errReviewProjectUnconfigured = errors.New("no project selected")

const reviewUnconfiguredHint = "PR reviews: no project selected (use --project or set default_project)"

func (m Model) refreshReviewSessions() tea.Msg {
	started := time.Now()
	if m.projectID <= 0 {
		return reviewSessionsMsg{after: m.reviewAfter, started: started, err: errReviewProjectUnconfigured}
	}
	if m.client == nil {
		return reviewSessionsMsg{after: m.reviewAfter, started: started, err: fmt.Errorf("configure authentication to view managed reviews")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, err := m.client.listReviewSessions(ctx, m.projectID, m.reviewAfter)
	return reviewSessionsMsg{page: page, after: m.reviewAfter, started: started, err: err}
}
func (m *Model) replaceSessionRows(rows []SessionRow) {
	name := ""
	if idx := m.selectedSessionIdx(); idx >= 0 && idx < len(m.sessions) {
		name = m.sessions[idx].Name
	}
	m.sessions = rows
	m.buildGroups()
	for idx, s := range rows {
		if s.Name == name {
			if !m.groupMode {
				m.cursor = idx
				return
			}
			pos := 0
			for _, root := range m.groupOrder {
				pos++
				if !m.collapsedGroups[root] {
					for _, i := range m.groupedSessions[root] {
						if i == idx {
							m.cursor = pos
							return
						}
						pos++
					}
				}
			}
		}
	}
	maxIdx := len(rows) - 1
	if m.groupMode {
		maxIdx = m.groupedListLen() - 1
	}
	if maxIdx < 0 {
		maxIdx = 0
	}
	if m.cursor > maxIdx {
		m.cursor = maxIdx
	}
}
func reviewShortSHA(s string) string {
	s = reviewDisplay(s)
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func renderReviewSession(s *reviewSession, width, height int) string {
	var lines = []string{reviewSessionLabel, s.pullRequest(), s.progress(), "Head " + reviewShortSHA(s.HeadSHA) + " · base " + reviewShortSHA(s.BaseSHA), "Runner: " + reviewDisplay(s.RunnerName) + " (" + reviewDisplay(s.RunnerKind) + ")", "Branch: " + reviewDisplay(s.GitBranch)}
	if s.LastContactAt > 0 {
		lines = append(lines, "Last contact: "+time.UnixMilli(s.LastContactAt).Local().Format("Jan 2 15:04:05"))
	}
	if s.CompletedAt > 0 {
		lines = append(lines, "Completed: "+time.UnixMilli(s.CompletedAt).Local().Format("Jan 2 15:04:05"))
	}
	lines = append(lines, reviewDisplay(s.PRURL), "Read-only. Enter: review details.")
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}
