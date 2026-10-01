package vibeflowcli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// veraDetailPages bounds the findings one review detail reads.
const veraDetailPages = 8 // ponytail: 200 findings; page on scroll if a review ever has more.

type reviewFinding struct {
	ID           string `json:"id"`
	JobID        string `json:"job_id"`
	Title        string `json:"title"`
	Severity     string `json:"severity"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	State        string `json:"state"`
	Trigger      string `json:"trigger"`
	Impact       string `json:"impact"`
	Evidence     string `json:"evidence"`
	Verification string `json:"verification"`
}

type reviewFindingsPage struct {
	Findings    []reviewFinding `json:"findings"`
	NextAfterID string          `json:"next_after_id"`
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
	for _, f := range page.Findings {
		if f.JobID != jobID {
			return reviewFindingsPage{}, fmt.Errorf("invalid finding scope")
		}
	}
	return page, nil
}

type reviewDetailMsg struct {
	summary  reviewSummary
	findings []reviewFinding
	err      error
}

// loadReviewDetail reads one review's recorded result and its findings.
func loadReviewDetail(ctx context.Context, client *Client, projectID int64, jobID string) tea.Msg {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s, err := client.getReviewSummary(ctx, projectID, jobID)
	if err != nil {
		return reviewDetailMsg{err: err}
	}
	msg := reviewDetailMsg{summary: s}
	after := ""
	for page := 0; page < veraDetailPages; page++ {
		p, err := client.listReviewSummaryFindings(ctx, projectID, jobID, after)
		if err != nil {
			return reviewDetailMsg{err: err}
		}
		msg.findings = append(msg.findings, p.Findings...)
		if after = p.NextAfterID; after == "" {
			break
		}
	}
	return msg
}

// reviewDetail is one review's read-only result: in a popup over Vera's
// session, or inside the history pane when popups are unavailable.
type reviewDetail struct {
	load          func() tea.Msg
	data          *reviewDetailMsg
	offset        int
	width, height int
}

func newReviewDetail(load func() tea.Msg) reviewDetail { return reviewDetail{load: load} }

// update handles one message; closed reports q or Esc.
func (d reviewDetail) update(msg tea.Msg) (_ reviewDetail, closed bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
	case reviewDetailMsg:
		d.data, d.offset = &msg, 0
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			d.scroll(-3)
		case tea.MouseWheelDown:
			d.scroll(3)
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc":
			return d, true
		case "up", "k":
			d.scroll(-1)
		case "down", "j":
			d.scroll(1)
		case "pgup":
			d.scroll(-d.page())
		case "pgdown", "space":
			d.scroll(d.page())
		case "home", "g":
			d.scroll(-len(d.lines()))
		case "end", "G":
			d.scroll(len(d.lines()))
		}
	}
	return d, false
}

func (d reviewDetail) size() (int, int) {
	if d.width == 0 {
		return 80, 24
	}
	return max(1, d.width), max(2, d.height)
}

// page is the body height above the footer line.
func (d reviewDetail) page() int { _, h := d.size(); return h - 1 }

func (d *reviewDetail) scroll(delta int) {
	d.offset = max(0, min(d.offset+delta, len(d.lines())-d.page()))
}

// reviewDetailText keeps a server string's lines, without terminal controls.
func reviewDetailText(s string) []string {
	var out []string
	for _, line := range strings.Split(ansi.Strip(s), "\n") {
		out = append(out, strings.Map(func(r rune) rune {
			if r == '\t' {
				return ' '
			}
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, line))
	}
	return out
}

// lines is the whole detail, wrapped to the width.
func (d reviewDetail) lines() []string {
	width, _ := d.size()
	var out []string
	add := func(indent, text string) {
		for _, line := range reviewDetailText(text) {
			wrapped := ansi.Wrap(line, max(1, width-len(indent)), "")
			for _, w := range strings.Split(wrapped, "\n") {
				out = append(out, indent+w)
			}
		}
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	switch {
	case d.data == nil:
		return []string{dim.Render("Loading review...")}
	case d.data.err != nil:
		return []string{lipgloss.NewStyle().Foreground(warningColor).Render(ansi.Truncate("Review details unavailable: "+reviewDisplay(d.data.err.Error()), width, "…"))}
	}
	s := d.data.summary
	r := s.Review
	for _, line := range strings.Split(ansi.Wrap(fmt.Sprintf("PR #%d · %s", r.Number, strings.Join(reviewDetailText(r.Details.Title), " ")), width, ""), "\n") {
		out = append(out, lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render(line))
	}
	add("", r.Details.URL)
	add("", "Outcome: "+reviewStateLabel(r.State))
	if r.RoundLimit > 0 {
		add("", fmt.Sprintf("Rounds: %d of %d used", r.RoundsStarted, r.RoundLimit))
	}
	head := ""
	if s.LastCompletedRound != nil {
		head = reviewShortSHA(s.LastCompletedRound.HeadSHA)
	}
	if head != "" {
		add("", "Last reviewed head: "+head[:min(7, len(head))])
	}
	for _, line := range strings.Split(ansi.Wrap("The live harness transcript of past reviews is not stored; this is the recorded result.", width, ""), "\n") {
		out = append(out, dim.Render(line))
	}
	if strings.TrimSpace(s.Summary) != "" {
		out = append(out, "", "Summary")
		add("  ", s.Summary)
	}
	out = append(out, "", fmt.Sprintf("Findings (%d)", len(d.data.findings)))
	for i, f := range d.data.findings {
		place := f.Path
		if f.Line > 0 {
			place = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		add("  ", fmt.Sprintf("%d. %s · %s · %s", i+1, f.State, f.Severity, place))
		add("     ", f.Title)
		add("     ", "Trigger: "+f.Trigger)
		add("     ", "Impact: "+f.Impact)
		add("     ", "Evidence: "+f.Evidence)
		add("     ", "Verification: "+f.Verification)
	}
	return out
}

func (d reviewDetail) render() string {
	width, height := d.size()
	lines := d.lines()
	start := min(d.offset, max(0, len(lines)-d.page()))
	body := lines[start:min(len(lines), start+d.page())]
	for len(body) < d.page() {
		body = append(body, "")
	}
	for i := range body {
		body[i] = ansi.Truncate(body[i], width, "…")
	}
	footer := lipgloss.NewStyle().Foreground(dimColor).Render(ansi.Truncate("↑↓ scroll · q/Esc close", width, "…"))
	return strings.Join(append(body[:height-1], footer), "\n")
}

// reviewDetailProgram runs a detail on its own, as the popup does.
type reviewDetailProgram struct{ d reviewDetail }

func (p reviewDetailProgram) Init() tea.Cmd { return p.d.load }
func (p reviewDetailProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		return p, tea.Quit
	}
	d, closed := p.d.update(msg)
	if closed {
		return p, tea.Quit
	}
	return reviewDetailProgram{d}, nil
}
func (p reviewDetailProgram) View() tea.View {
	v := tea.NewView(p.d.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// runReviewDetail shows one review until closed.
func runReviewDetail(ctx context.Context, cfg *Config, projectID int64, jobID string) error {
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	d := newReviewDetail(func() tea.Msg { return loadReviewDetail(ctx, client, projectID, jobID) })
	_, err := tea.NewProgram(reviewDetailProgram{d}, tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}
