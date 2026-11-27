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

	// The live section (#5599): shown while the review runs on this machine.
	live       func(prNumber int) tea.Msg // nil: no live view
	liveData   *reviewLiveMsg
	liveOffset int
	liveFollow bool // keep the newest output in view until the user scrolls up
	focus      int  // 0 the result, 1 the live section
}

func newReviewDetail(load func() tea.Msg) reviewDetail {
	return reviewDetail{load: load, liveFollow: true}
}

// withLive adds the live section's loader.
func (d reviewDetail) withLive(live func(prNumber int) tea.Msg) reviewDetail {
	d.live = live
	return d
}

// start loads the review and, when there is one, its live view.
func (d reviewDetail) start() tea.Cmd {
	if d.live == nil {
		return d.load
	}
	return tea.Batch(d.load, d.liveCmd())
}

func (d reviewDetail) liveCmd() tea.Cmd {
	n := 0
	if d.data != nil {
		n = int(d.data.summary.Review.Number)
	}
	live := d.live
	return func() tea.Msg { return live(n) }
}

// followUp is the command a message calls for: the live view refreshes itself
// every veraLiveRefresh for as long as the detail is open.
func (d reviewDetail) followUp(msg tea.Msg) tea.Cmd {
	if d.live == nil {
		return nil
	}
	switch msg.(type) {
	case reviewLiveMsg:
		return tea.Tick(veraLiveRefresh, func(time.Time) tea.Msg { return reviewLiveTickMsg{} })
	case reviewLiveTickMsg:
		return d.liveCmd()
	}
	return nil
}

// showLive reports whether the live section is on screen: the running
// harness, or the last attempt's saved transcript.
func (d reviewDetail) showLive() bool {
	return d.liveData != nil && (d.liveData.running || len(d.liveData.saved) > 0 || len(d.liveData.events) > 0)
}

// update handles one message; closed reports q or Esc.
func (d reviewDetail) update(msg tea.Msg) (_ reviewDetail, closed bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
	case reviewDetailMsg:
		d.data, d.offset = &msg, 0
	case reviewLiveMsg:
		d.liveData = &msg
		if !d.showLive() {
			d.focus = 0
		}
		if d.liveFollow {
			d.liveOffset = d.liveMax()
		}
		d.liveOffset = min(d.liveOffset, d.liveMax())
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft && d.showLive() {
			d.focus = d.sectionAt(msg.Y)
		}
	case tea.MouseWheelMsg:
		section := d.sectionAt(msg.Y)
		switch msg.Button {
		case tea.MouseWheelUp:
			d.scrollSection(section, -3)
		case tea.MouseWheelDown:
			d.scrollSection(section, 3)
		}
	case tea.KeyPressMsg:
		page := d.sectionPage(d.focus)
		switch msg.String() {
		case "q", "esc":
			return d, true
		case "tab", "shift+tab":
			if d.showLive() {
				d.focus = 1 - d.focus
			}
		case "up", "k":
			d.scrollSection(d.focus, -1)
		case "down", "j":
			d.scrollSection(d.focus, 1)
		case "pgup":
			d.scrollSection(d.focus, -page)
		case "pgdown", "space":
			d.scrollSection(d.focus, page)
		case "home", "g":
			d.scrollSection(d.focus, -1<<20)
		case "end", "G":
			d.scrollSection(d.focus, 1<<20)
		}
	}
	return d, false
}

// resultPage is how many result lines show: the whole body without a live
// section, otherwise the top two fifths (at least 3 lines).
func (d reviewDetail) resultPage() int {
	if !d.showLive() {
		return d.page()
	}
	return max(3, d.page()*2/5)
}

// livePage is how many live lines show below the live section's header.
func (d reviewDetail) livePage() int {
	if !d.showLive() {
		return 0
	}
	return max(1, d.page()-d.resultPage()-1)
}

func (d reviewDetail) sectionPage(section int) int {
	if section == 1 {
		return d.livePage()
	}
	return d.resultPage()
}

// sectionAt is the section under screen row y.
func (d reviewDetail) sectionAt(y int) int {
	if d.showLive() && y >= d.resultPage() {
		return 1
	}
	return 0
}

func (d reviewDetail) liveMax() int { return max(0, len(d.liveLines())-d.livePage()) }

func (d *reviewDetail) scrollSection(section, delta int) {
	if section == 1 && d.showLive() {
		d.liveOffset = max(0, min(d.liveOffset+delta, d.liveMax()))
		d.liveFollow = d.liveOffset >= d.liveMax()
		return
	}
	d.scroll(delta)
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
	d.offset = max(0, min(d.offset+delta, len(d.lines())-d.resultPage()))
}

// liveLines is the live section: this PR's progress, then the harness's
// current screen.
func (d reviewDetail) liveLines() []string {
	if !d.showLive() {
		return nil
	}
	dim := lipgloss.NewStyle().Foreground(dimColor)
	var out []string
	for _, line := range d.liveData.progress {
		out = append(out, dim.Render(line))
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	if len(d.liveData.events) > 0 {
		out = append(out, lipgloss.NewStyle().Bold(true).Render("Runner events"))
		for _, line := range d.liveData.events {
			out = append(out, dim.Render(line))
		}
		out = append(out, "")
	}
	if !d.liveData.running {
		out = append(out, lipgloss.NewStyle().Bold(true).Render("Harness output of the last attempt"))
		if len(d.liveData.saved) == 0 {
			return append(out, dim.Render("(none was captured)"))
		}
		return append(out, d.liveData.saved...)
	}
	out = append(out, lipgloss.NewStyle().Bold(true).Render("Harness output"))
	return append(out, d.liveData.output...)
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
	if !d.showLive() {
		for _, line := range strings.Split(ansi.Wrap("The live harness transcript of past reviews is not stored; this is the recorded result.", width, ""), "\n") {
			out = append(out, dim.Render(line))
		}
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
	window := func(lines []string, offset, n int) []string {
		start := min(offset, max(0, len(lines)-n))
		out := append([]string(nil), lines[start:min(len(lines), start+n)]...)
		for len(out) < n {
			out = append(out, "")
		}
		for i := range out {
			out[i] = ansi.Truncate(out[i], width, "…")
		}
		return out
	}
	body := window(d.lines(), d.offset, d.resultPage())
	hint := "↑↓ scroll · q/Esc close"
	if d.showLive() {
		state := "following"
		if !d.liveFollow {
			state = "paused · End follows"
		}
		header := fmt.Sprintf(" Live · %s · refreshes every %ds ", state, int(veraLiveRefresh.Seconds()))
		if !d.liveData.running {
			header = " Last attempt · recorded on this machine "
			if !d.liveData.savedAt.IsZero() {
				header = fmt.Sprintf(" Last attempt · saved %s on this machine ", d.liveData.savedAt.Local().Format("Jan 2 15:04"))
			}
		}
		header = ansi.Truncate(header, width, "…")
		style := lipgloss.NewStyle().Foreground(dimColor)
		if d.focus == 1 {
			style = lipgloss.NewStyle().Bold(true).Reverse(true)
		}
		body = append(body, style.Render(header))
		body = append(body, window(d.liveLines(), d.liveOffset, d.livePage())...)
		hint = "Tab or click: switch section · ↑↓ scroll · q/Esc close"
	}
	for len(body) < height-1 {
		body = append(body, "")
	}
	footer := lipgloss.NewStyle().Foreground(dimColor).Render(ansi.Truncate(hint, width, "…"))
	return strings.Join(append(body[:height-1], footer), "\n")
}

// reviewDetailProgram runs a detail on its own, as the popup does.
type reviewDetailProgram struct{ d reviewDetail }

func (p reviewDetailProgram) Init() tea.Cmd { return p.d.start() }
func (p reviewDetailProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		return p, tea.Quit
	}
	d, closed := p.d.update(msg)
	if closed {
		return p, tea.Quit
	}
	return reviewDetailProgram{d}, d.followUp(msg)
}
func (p reviewDetailProgram) View() tea.View {
	v := tea.NewView(p.d.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// runReviewDetail shows one review until closed; o is the Vera console's,
// whose progress log and tmux server feed the live section.
func runReviewDetail(ctx context.Context, cfg *Config, o reviewWatchOptions, jobID string) error {
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	d := newReviewDetail(func() tea.Msg { return loadReviewDetail(ctx, client, o.ProjectID, jobID) }).
		withLive(veraLiveLoader(cfg.ServerURL, o, jobID))
	_, err := tea.NewProgram(reviewDetailProgram{d}, tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// veraLiveLoader reads a review's live view from Vera's tmux server.
func veraLiveLoader(serverURL string, o reviewWatchOptions, jobID string) func(int) tea.Msg {
	logPath := reviewProgressLogPath(serverURL, o)
	return func(prNumber int) tea.Msg { return loadReviewLive(execVeraTmux, logPath, jobID, prNumber) }
}
