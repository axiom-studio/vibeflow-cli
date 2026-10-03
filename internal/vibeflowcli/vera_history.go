package vibeflowcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Vera's tmux session runs the listener on the left and this read-only list
// of the repository's past reviews on the right.
const (
	veraHistoryWidth   = "30%"
	veraHistoryRefresh = 10 * time.Second
	veraHistoryPages   = 4  // Up to 400 of the repository's newest review attempts...
	veraHistoryReviews = 50 // ...naming at most this many reviews.
)

type reviewHistoryMsg struct {
	rows    []reviewSummary
	current string // Job this Vera is reviewing now, from its local receipt.
	err     error
	at      time.Time
}
type reviewHistoryTickMsg struct{}

// reviewPopupMsg reports a closed review popup, or why none could open.
type reviewPopupMsg struct {
	job string
	err error
}

type reviewHistory struct {
	load           func() tea.Msg
	title          string
	rows           []reviewSummary
	current        string
	status         string
	failed, loaded bool
	cursor, offset int
	width, height  int
	popup          func(job string) error   // Shows one review over the session.
	loadDetail     func(job string) tea.Msg // Reads one review for the in-pane fallback.
	detail         *reviewDetail            // The in-pane review, when popups are unavailable.
}

func newReviewHistory(title string, load func() tea.Msg) reviewHistory {
	return reviewHistory{load: load, title: title, status: "Loading reviews..."}
}

// reviewHistoryRows keeps one repository binding's reviews, newest first.
func reviewHistoryRows(summaries []reviewSummary, link int64, provider string) []reviewSummary {
	var rows []reviewSummary
	for _, s := range summaries {
		if s.Review.RepositoryLinkID == link && s.Review.Provider == provider {
			rows = append(rows, s)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return reviewHistoryTime(rows[i]) > reviewHistoryTime(rows[j]) })
	return rows
}

// reviewHistoryTime is the latest attempt activity, in Unix milliseconds.
func reviewHistoryTime(s reviewSummary) int64 {
	var t int64
	for _, v := range s.ReviewSessions {
		for _, at := range []int64{v.StartedAt, v.CompletedAt} {
			if at > t {
				t = at
			}
		}
	}
	return t
}

// reviewHistoryLoader reads this binding's reviews and the job its listener
// is reviewing now. The project's summary list is in job-ID order, not by
// time, so the reviews are found through the server's repository filter on
// review attempts, newest first, and each summary is read on its own. A
// summary is re-read only when its review has a new or active attempt.
type reviewHistoryLoader struct {
	client    *Client
	serverURL string
	o         reviewWatchOptions
	mu        sync.Mutex
	cache     map[string]reviewHistoryEntry
}

type reviewHistoryEntry struct {
	attempt string // The review's newest attempt as last read.
	summary reviewSummary
}

func newReviewHistoryLoader(client *Client, serverURL string, o reviewWatchOptions) *reviewHistoryLoader {
	return &reviewHistoryLoader{client: client, serverURL: serverURL, o: o, cache: map[string]reviewHistoryEntry{}}
}

func (l *reviewHistoryLoader) load() tea.Msg {
	l.mu.Lock()
	defer l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o := l.o
	var jobs []string
	attempts := map[string]string{} // Job -> its newest attempt.
	active := map[string]bool{}
	after := ""
	for page := 0; page < veraHistoryPages && len(jobs) < veraHistoryReviews; page++ {
		p, err := l.client.listRepositoryReviewSessions(ctx, o.ProjectID, o.GitProvider, o.RepositoryLinkID, after)
		if err != nil {
			return reviewHistoryMsg{err: err, at: time.Now()}
		}
		for _, s := range p.Sessions {
			// A server without the filter lists every repository.
			if s.Provider != o.GitProvider || s.RepositoryLinkID != o.RepositoryLinkID || !reviewPublicID(s.JobID) {
				continue
			}
			if _, seen := attempts[s.JobID]; !seen {
				attempts[s.JobID] = fmt.Sprint(s.SessionID, s.State, s.CompletedAt)
				active[s.JobID] = s.Active
				jobs = append(jobs, s.JobID)
			}
		}
		if after = p.NextAfterID; after == "" {
			break
		}
	}
	jobs = jobs[:min(len(jobs), veraHistoryReviews)]
	var rows []reviewSummary
	keep := map[string]bool{}
	for _, job := range jobs {
		keep[job] = true
		e, ok := l.cache[job]
		if !ok || e.attempt != attempts[job] || active[job] { // A running review's progress moves.
			s, err := l.client.getReviewSummary(ctx, o.ProjectID, job)
			if err != nil {
				return reviewHistoryMsg{err: err, at: time.Now()}
			}
			e = reviewHistoryEntry{attempt: attempts[job], summary: s}
			l.cache[job] = e // Kept even if a later read fails, so retries progress.
		}
		rows = append(rows, e.summary)
	}
	for job := range l.cache {
		if !keep[job] {
			delete(l.cache, job)
		}
	}
	current := ""
	if p := veraPendingReceipt(l.serverURL, o); p != nil {
		current = p.JobID
	}
	return reviewHistoryMsg{rows: reviewHistoryRows(rows, o.RepositoryLinkID, o.GitProvider), current: current, at: time.Now()}
}

func (h reviewHistory) Init() tea.Cmd { return h.load }

// open shows the selected review in a popup.
func (h reviewHistory) open() tea.Cmd {
	if h.cursor >= len(h.rows) || h.popup == nil {
		return nil
	}
	job, popup := h.rows[h.cursor].Review.ID, h.popup
	return func() tea.Msg { return reviewPopupMsg{job: job, err: popup(job)} }
}

func (h reviewHistory) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		h.width, h.height = size.Width, size.Height
	}
	if popup, ok := msg.(reviewPopupMsg); ok && popup.err != nil && h.loadDetail != nil {
		// No popup (tmux before 3.2, or no attached client): show it here.
		d := newReviewDetail(func() tea.Msg { return h.loadDetail(popup.job) })
		d, _ = d.update(tea.WindowSizeMsg{Width: h.width, Height: h.height})
		h.detail = &d
		return h, d.load
	}
	if h.detail != nil {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseWheelMsg, tea.WindowSizeMsg, reviewDetailMsg:
			if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
				return h, tea.Quit
			}
			d, closed := h.detail.update(msg)
			h.detail = &d
			if closed {
				h.detail = nil
			}
			return h, nil
		}
	}
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		// Two lines per entry below the header and status lines.
		if msg.Button == tea.MouseLeft && msg.Y >= 2 {
			if i := h.offset + (msg.Y-2)/2; i < min(len(h.rows), h.offset+h.page()) {
				if i == h.cursor {
					return h, h.open()
				}
				h.scroll(i - h.cursor)
			}
		}
	case reviewHistoryTickMsg:
		return h, h.load
	case reviewHistoryMsg:
		next := tea.Tick(veraHistoryRefresh, func(time.Time) tea.Msg { return reviewHistoryTickMsg{} })
		if msg.err != nil {
			h.failed, h.status = true, "Review API unavailable; retrying: "+reviewDisplay(msg.err.Error())
			return h, next
		}
		selected := ""
		if h.cursor < len(h.rows) {
			selected = h.rows[h.cursor].Review.ID
		}
		h.rows, h.current, h.failed, h.loaded = msg.rows, msg.current, false, true
		h.status = "Updated " + msg.at.Local().Format("15:04:05")
		h.cursor = 0
		for i, s := range h.rows {
			if s.Review.ID == selected {
				h.cursor = i
			}
		}
		h.scroll(0)
		return h, next
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			h.scroll(-1)
		case tea.MouseWheelDown:
			h.scroll(1)
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return h, tea.Quit
		case "up", "k":
			h.scroll(-1)
		case "down", "j":
			h.scroll(1)
		case "pgup":
			h.scroll(-h.page())
		case "pgdown", "space":
			h.scroll(h.page())
		case "home", "g":
			h.scroll(-len(h.rows))
		case "end", "G":
			h.scroll(len(h.rows))
		case "enter":
			return h, h.open()
		}
	}
	return h, nil
}

// page is how many two-line entries fit between the header, status and
// footer lines.
func (h reviewHistory) page() int { return max(1, (h.height-3)/2) }

func (h *reviewHistory) scroll(delta int) {
	h.cursor = max(0, min(len(h.rows)-1, h.cursor+delta))
	if h.cursor < h.offset {
		h.offset = h.cursor
	}
	if h.cursor >= h.offset+h.page() {
		h.offset = h.cursor - h.page() + 1
	}
	h.offset = max(0, min(h.offset, len(h.rows)-h.page()))
}

func (h reviewHistory) View() tea.View {
	v := tea.NewView(h.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (h reviewHistory) render() string {
	if h.detail != nil {
		return h.detail.render()
	}
	width, height := max(1, h.width), max(3, h.height)
	if h.width == 0 {
		width, height = 40, 24
	}
	fit := func(s string) string { return ansi.Truncate(s, width, "…") }
	dim := lipgloss.NewStyle().Foreground(dimColor)
	status := dim.Render(fit(h.status))
	if h.failed {
		status = lipgloss.NewStyle().Foreground(warningColor).Render(fit(h.status))
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render(fit("Reviews · " + h.title)), status}
	if len(h.rows) == 0 && h.loaded {
		lines = append(lines, dim.Render(fit("No reviews yet.")))
	}
	for i := h.offset; i < min(len(h.rows), h.offset+h.page()); i++ {
		s := h.rows[i]
		// ">" marks the cursor even without colors (NO_COLOR drops reverse).
		marker := " "
		if i == h.cursor {
			marker = ">"
		}
		if s.Review.ID == h.current {
			marker += "▶ "
		} else {
			marker += "  "
		}
		title := fit(fmt.Sprintf("%s#%d %s", marker, s.Review.Number, reviewDisplay(s.Review.Details.Title)))
		meta := fit("   " + reviewHistoryMeta(s))
		switch {
		case i == h.cursor:
			style := lipgloss.NewStyle().Reverse(true)
			title, meta = style.Render(title), style.Render(meta)
		case s.Review.ID == h.current:
			title = lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render(title)
		default:
			meta = dim.Render(meta)
		}
		lines = append(lines, title, meta)
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	return strings.Join(append(lines[:height-1], dim.Render(fit(veraHistoryHint(width)))), "\n")
}

// veraHistoryHint is the longest key hint that fits the pane: about 30% of
// the window is often under 40 columns.
func veraHistoryHint(width int) string {
	for _, hint := range []string{"↑↓ browse · Enter: open review · click works", "↑↓ browse · Enter: open review", "↑↓ · Enter: open"} {
		if ansi.StringWidth(hint) <= width {
			return hint
		}
	}
	return "Enter: open"
}

// reviewHistoryMeta is a review's outcome line: state, round, findings,
// short head and the time of its latest attempt.
func reviewHistoryMeta(s reviewSummary) string {
	parts := []string{reviewStateLabel(s.Review.State)}
	if s.Progress != nil && s.Progress.RoundNumber > 0 {
		parts = append(parts, fmt.Sprintf("round %d", s.Progress.RoundNumber))
	}
	findings := "findings"
	if s.FindingCount == 1 {
		findings = "finding"
	}
	parts = append(parts, fmt.Sprintf("%d %s", s.FindingCount, findings))
	if sha := reviewShortSHA(s.Review.HeadSHA); sha != "" {
		parts = append(parts, sha[:min(7, len(sha))])
	}
	if t := reviewHistoryTime(s); t > 0 {
		parts = append(parts, time.UnixMilli(t).Local().Format("Jan 2 15:04"))
	}
	return strings.Join(parts, " · ")
}

// runReviewHistory runs the history list in the terminal until interrupted.
func runReviewHistory(ctx context.Context, cfg *Config, o reviewWatchOptions) error {
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	model := newReviewHistory(filepath.Base(o.Repository), newReviewHistoryLoader(client, cfg.ServerURL, o).load)
	model.popup = func(job string) error {
		args := veraDetailArgs(os.Args, job)
		bin, err := cliExecutable()
		if err != nil {
			return err
		}
		args[0] = bin
		return exec.Command("tmux", veraPopupArgs("exec "+shellJoin(args))...).Run()
	}
	model.loadDetail = func(job string) tea.Msg { return loadReviewDetail(ctx, client, o.ProjectID, job) }
	_, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}
