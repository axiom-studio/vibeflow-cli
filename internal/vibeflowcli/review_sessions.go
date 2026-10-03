package vibeflowcli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const reviewSessionLabel = "Vera · Code Reviewer"

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
	Title            string `json:"-"`
	FindingCount     int    `json:"-"`
	BlockerCount     int    `json:"-"`
	CountsKnown      bool   `json:"-"`
	Stage            string `json:"-"`
}

type reviewSessionsPage struct {
	Sessions    []reviewSession `json:"sessions"`
	NextAfterID string          `json:"next_after_id"`
}

type reviewSummaryJob struct {
	reviewJob
	ProjectID     int64 `json:"project_id"`
	Number        int64 `json:"number"`
	RoundsStarted int   `json:"rounds_started"`
	RoundLimit    int   `json:"round_limit"`
	Details       struct {
		URL                string `json:"url"`
		BaseRepositoryName string `json:"base_repository_name"`
		Title              string `json:"title"`
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
	LastCompletedRound *struct {
		Number  int    `json:"number"`
		HeadSHA string `json:"head_sha"`
	} `json:"last_completed_round"`
	ReviewSessions     []reviewSession `json:"review_sessions"`
	ReviewSessionsNext string          `json:"review_sessions_next_after_id"`
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
func (c *Client) listReviewSessions(ctx context.Context, projectID int64, after string) (reviewSessionsPage, error) {
	return c.reviewSessionsPage(ctx, projectID, url.Values{"limit": {"25"}}, after, 25)
}

// listRepositoryReviewSessions pages one repository binding's review
// attempts, newest first, using the server's repository filter.
func (c *Client) listRepositoryReviewSessions(ctx context.Context, projectID int64, provider string, link int64, after string) (reviewSessionsPage, error) {
	query := url.Values{"limit": {"100"}, "provider": {provider}, "repository_link_id": {strconv.FormatInt(link, 10)}}
	return c.reviewSessionsPage(ctx, projectID, query, after, 100)
}

func (c *Client) reviewSessionsPage(ctx context.Context, projectID int64, query url.Values, after string, limit int) (reviewSessionsPage, error) {
	var page reviewSessionsPage
	if projectID <= 0 || len(after) > 256 {
		return page, fmt.Errorf("select a valid project and review history cursor")
	}
	if after != "" {
		query.Set("after_id", after)
	}
	err := c.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-sessions?%s", projectID, query.Encode()), nil, &page)
	if err != nil {
		return page, err
	}
	if len(page.Sessions) > limit || len(page.NextAfterID) > 256 || (page.NextAfterID != "" && page.NextAfterID == after) {
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
	state := reviewStateLabel(s.State)
	if s.Stage != "" && s.Stage != s.State && (s.State == "queued" || s.State == "reviewing") {
		state += " · " + reviewDisplay(strings.ReplaceAll(s.Stage, "_", " "))
	}
	if s.RoundNumber > 0 {
		state += fmt.Sprintf(" · round %d", s.RoundNumber)
	}
	if s.AttemptNumber > 0 {
		state += fmt.Sprintf(" · attempt %d", s.AttemptNumber)
	}
	return state
}
func reviewStateLabel(state string) string {
	switch state {
	case "queued":
		return "Queued"
	case "reviewing":
		return "Reviewing"
	case "changes_requested":
		return "Changes requested"
	case "clean":
		return "Clean"
	case "needs_human":
		return "Needs human review"
	case "paused":
		return "Paused"
	case "disabled":
		return "Disabled"
	case "closed":
		return "Closed"
	default:
		return reviewDisplay(strings.ReplaceAll(state, "_", " "))
	}
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

func reviewShortSHA(s string) string {
	s = reviewDisplay(s)
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
