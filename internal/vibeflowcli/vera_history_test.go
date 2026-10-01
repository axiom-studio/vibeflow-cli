package vibeflowcli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func historySummary(id string, number, link int64, provider, state string, round, findings int, at int64) reviewSummary {
	s := reviewSummary{FindingCount: findings, Progress: &reviewProgress{RoundNumber: round}}
	s.Review.ID, s.Review.Number, s.Review.RepositoryLinkID, s.Review.Provider, s.Review.State = id, number, link, provider, state
	s.Review.HeadSHA = strings.Repeat(id[:1], 40)
	s.Review.Details.Title = "Title " + id
	s.Review.Details.URL = "https://github.com/acme/repo/pull/" + fmt.Sprint(number)
	if at > 0 {
		s.ReviewSessions = []reviewSession{{StartedAt: at - 1000, CompletedAt: at}}
	}
	return s
}

// Only this Vera's repository binding is listed, newest review first.
func TestReviewHistoryRowsFilterAndSort(t *testing.T) {
	got := reviewHistoryRows([]reviewSummary{
		historySummary("a", 1, 7, "github", "clean", 1, 0, 1000),
		historySummary("b", 2, 8, "github", "clean", 1, 0, 5000),    // other repository
		historySummary("c", 3, 7, "bitbucket", "clean", 1, 0, 6000), // same link ID, other integration
		historySummary("d", 4, 7, "github", "changes_requested", 2, 3, 9000),
		historySummary("e", 5, 7, "github", "queued", 0, 0, 0), // not started yet
	}, 7, "github")
	var ids []string
	for _, s := range got {
		ids = append(ids, s.Review.ID)
	}
	if strings.Join(ids, ",") != "d,a,e" {
		t.Fatalf("history rows %v; want d,a,e", ids)
	}
}

func historyModel(t *testing.T, width, height int, rows []reviewSummary, current string) reviewHistory {
	t.Helper()
	h := newReviewHistory("acme/repo", func() tea.Msg { return nil })
	next, _ := h.Update(tea.WindowSizeMsg{Width: width, Height: height})
	next, cmd := next.(reviewHistory).Update(reviewHistoryMsg{rows: rows, current: current, at: time.Date(2026, 9, 30, 17, 51, 0, 0, time.Local)})
	if cmd == nil {
		t.Fatal("history did not schedule its next refresh")
	}
	return next.(reviewHistory)
}

func historyKey(t *testing.T, h reviewHistory, keys ...string) reviewHistory {
	t.Helper()
	for _, key := range keys {
		code := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "home": tea.KeyHome, "end": tea.KeyEnd, "enter": tea.KeyEnter}[key]
		next, _ := h.Update(tea.KeyPressMsg{Code: code})
		h = next.(reviewHistory)
	}
	return h
}

func checkHistoryFits(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Fatalf("view has %d lines for height %d:\n%s", len(lines), height, view)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			t.Fatalf("line wider than %d: %q", width, ansi.Strip(line))
		}
	}
}

func TestReviewHistoryViewHighlightsCurrentAndScrolls(t *testing.T) {
	var rows []reviewSummary
	for i := 0; i < 10; i++ {
		state := []string{"clean", "changes_requested", "needs_human"}[i%3]
		rows = append(rows, historySummary(fmt.Sprintf("%c", 'a'+i), int64(40+i), 7, "github", state, i%3+1, i, int64(10000-i)))
	}
	rows[0].Review.Details.Title = "Add late payment fee calculation with a title far too long for a narrow pane"
	h := historyModel(t, 60, 12, rows, "b")
	view := ansi.Strip(h.render())
	checkHistoryFits(t, h.render(), 60, 12)
	for _, want := range []string{"acme/repo", "#40 Add late payment", "Clean · round 1 · 0 findings · aaaaaaa", "▶ #41 Title b", "Changes requested · round 2 · 1 finding", "Needs human review"} {
		if !strings.Contains(view, want) {
			t.Fatalf("history view lacks %q:\n%s", want, view)
		}
	}
	// 12 rows: header, status, footer and 9 lines -> 4 two-line entries.
	h = historyKey(t, h, "down", "down", "down", "down", "down", "down")
	view = ansi.Strip(h.render())
	if h.cursor != 6 || !strings.Contains(view, "#46 Title g") || strings.Contains(view, "#40 ") {
		t.Fatalf("cursor %d did not scroll into view:\n%s", h.cursor, view)
	}
	h = historyKey(t, h, "end")
	if h.cursor != 9 || !strings.Contains(ansi.Strip(h.render()), "#49 Title j") {
		t.Fatalf("end did not reach the oldest review: %d", h.cursor)
	}
	h = historyKey(t, h, "pgup")
	if h.cursor != 5 {
		t.Fatalf("page up moved to %d; want 5", h.cursor)
	}
	h = historyKey(t, h, "home", "pgdown")
	if h.cursor != 4 {
		t.Fatalf("page down moved to %d; want 4", h.cursor)
	}
	next, _ := h.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	next, _ = next.(reviewHistory).Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	next, _ = next.(reviewHistory).Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	h = next.(reviewHistory)
	if h.cursor != 5 {
		t.Fatalf("mouse wheel moved to %d; want 5", h.cursor)
	}
	h = historyKey(t, h, "enter")
	if view := ansi.Strip(h.render()); !strings.Contains(view, "https://github.com/acme/repo/pull/45") {
		t.Fatalf("enter did not show the PR link:\n%s", view)
	}
	// A refresh keeps the selected PR selected even when a newer one arrives.
	newer := append([]reviewSummary{historySummary("z", 50, 7, "github", "reviewing", 1, 0, 20000)}, rows...)
	next, _ = h.Update(reviewHistoryMsg{rows: newer, current: "z", at: time.Now()})
	if h = next.(reviewHistory); h.cursor != 6 || h.rows[h.cursor].Review.ID != "f" {
		t.Fatalf("refresh moved the selection to %d", h.cursor)
	}
	for _, width := range []int{12, 24} {
		narrow := historyModel(t, width, 8, rows, "b")
		checkHistoryFits(t, narrow.render(), width, 8)
	}
}

// An API outage replaces the status line; it never stacks messages and keeps
// the last good list on screen.
func TestReviewHistoryErrorIsOneStatusLine(t *testing.T) {
	h := historyModel(t, 60, 12, []reviewSummary{historySummary("a", 4, 7, "github", "clean", 2, 1, 1000)}, "")
	for i := 0; i < 3; i++ {
		next, cmd := h.Update(reviewHistoryMsg{err: fmt.Errorf("review API returned HTTP 503"), at: time.Now()})
		if cmd == nil {
			t.Fatal("history stopped refreshing after an error")
		}
		h = next.(reviewHistory)
	}
	view := ansi.Strip(h.render())
	if strings.Count(view, "unavailable") != 1 || !strings.Contains(view, "#4 Title a") {
		t.Fatalf("error status:\n%s", view)
	}
	next, _ := h.Update(reviewHistoryMsg{rows: h.rows, at: time.Now()})
	if view := ansi.Strip(next.(reviewHistory).render()); strings.Contains(view, "unavailable") || !strings.Contains(view, "Updated") {
		t.Fatalf("recovered status:\n%s", view)
	}
	empty := historyModel(t, 60, 12, nil, "")
	if view := ansi.Strip(empty.render()); !strings.Contains(view, "No reviews yet") {
		t.Fatalf("empty history:\n%s", view)
	}
}
