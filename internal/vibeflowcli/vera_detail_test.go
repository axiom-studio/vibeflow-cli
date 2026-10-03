package vibeflowcli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Keys typed in Vera's idle listener pane become tmux key names for its
// history pane; anything else is dropped instead of echoed.
func TestVeraBrowseKeys(t *testing.T) {
	for in, want := range map[string][]string{
		"\x1b[A\x1b[B":          {"Up", "Down"},
		"\x1bOA\x1bOB":          {"Up", "Down"},
		"kj":                    {"Up", "Down"},
		"\x1b[5~\x1b[6~":        {"PPage", "NPage"},
		"\x1b[H\x1b[F":          {"Home", "End"},
		"\x1b[1~\x1b[4~":        {"Home", "End"},
		"\r":                    {"Enter"},
		"\n":                    {"Enter"},
		"\x1b":                  {"Escape"},
		"q":                     {"q"},
		"\x1b[C\x1b[D\x1b[1;5A": nil, // right, left, ctrl-up: no browse meaning
		"hello":                 nil,
	} {
		if got := veraBrowseKeys([]byte(in)); !slices.Equal(got, want) {
			t.Errorf("veraBrowseKeys(%q) = %v; want %v", in, got, want)
		}
	}
}

type fakeVeraTmux struct {
	panes string
	calls [][]string
}

func (f *fakeVeraTmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if args[0] == "list-panes" {
		return f.panes, nil
	}
	return "", nil
}

func TestForwardVeraKeysToHistoryPane(t *testing.T) {
	tm := &fakeVeraTmux{panes: "%1\t\n%2\t1\n"}
	forwardVeraKeys(tm.run, "%1", []byte("\x1b[Bj\r"))
	want := [][]string{{"list-panes", "-t", "%1", "-F", "#{pane_id}\t#{@vibeflow_vera_history}"}, {"send-keys", "-t", "%2", "Down", "Down", "Enter"}}
	if fmt.Sprint(tm.calls) != fmt.Sprint(want) {
		t.Fatalf("tmux calls %q; want %q", tm.calls, want)
	}

	// Nothing to browse: no tmux call for other input, outside tmux, or
	// without a history pane in the window.
	tm = &fakeVeraTmux{panes: "%1\t\n"}
	forwardVeraKeys(tm.run, "%1", []byte("xyz"))
	forwardVeraKeys(tm.run, "", []byte("\x1b[B"))
	forwardVeraKeys(tm.run, "%1", []byte("\x1b[B"))
	if len(tm.calls) != 1 || tm.calls[0][0] != "list-panes" {
		t.Fatalf("unexpected tmux calls %q", tm.calls)
	}
}

func TestVeraPopupAndDetailCommand(t *testing.T) {
	if got := veraPopupArgs("exec vf"); !slices.Equal(got, []string{"display-popup", "-E", "-w", "90%", "-h", "85%", "exec vf"}) {
		t.Fatalf("popup args %q", got)
	}
	args := []string{"/bin/vf", "--root", "/r", "review-watch", "--history", "--project", "66", "--repository-link", "7"}
	want := []string{"/bin/vf", "--root", "/r", "review-watch", "--review-detail", "job-4", "--project", "66", "--repository-link", "7"}
	if got := veraDetailArgs(args, "job-4"); !slices.Equal(got, want) {
		t.Fatalf("detail args %q; want %q", got, want)
	}
	if !slices.Contains(args, "--history") {
		t.Fatal("veraDetailArgs changed its input")
	}
}

// Enter opens the selected review in a popup; without popups the detail
// shows in the pane until Esc.
func TestReviewHistoryEnterOpensReview(t *testing.T) {
	rows := []reviewSummary{historySummary("a", 4, 7, "github", "clean", 2, 1, 2000), historySummary("b", 5, 7, "github", "clean", 1, 0, 1000)}
	h := historyModel(t, 60, 12, rows, "")
	var opened []string
	h.popup = func(job string) error { opened = append(opened, job); return nil }
	h.loadDetail = func(job string) tea.Msg { return reviewDetailMsg{summary: rows[1]} }
	if !strings.Contains(ansi.Strip(h.render()), "↑↓ browse · Enter: open review · click works") {
		t.Fatalf("history footer:\n%s", ansi.Strip(h.render()))
	}

	h = historyKey(t, h, "down")
	next, cmd := h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next, _ = next.(reviewHistory).Update(cmd())
	h = next.(reviewHistory)
	if !slices.Equal(opened, []string{"b"}) || h.detail != nil {
		t.Fatalf("popup opened %v, in-pane detail %v", opened, h.detail != nil)
	}

	h.popup = func(string) error { return errors.New("no popups") }
	next, cmd = h.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next, cmd = next.(reviewHistory).Update(cmd())
	next, _ = next.(reviewHistory).Update(cmd())
	h = next.(reviewHistory)
	if view := ansi.Strip(h.render()); h.detail == nil || !strings.Contains(view, "PR #5 · Title b") || !strings.Contains(view, "Esc") {
		t.Fatalf("fallback detail:\n%s", view)
	}
	next, _ = h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h = next.(reviewHistory); h.detail != nil || !strings.Contains(ansi.Strip(h.render()), "Reviews · acme/repo") {
		t.Fatalf("Esc did not return to the list:\n%s", ansi.Strip(h.render()))
	}

	// A click selects a row; a click on the selected row opens it.
	opened = nil
	h.popup = func(job string) error { opened = append(opened, job); return nil }
	next, _ = h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, Y: 2})
	h = next.(reviewHistory)
	if h.cursor != 0 {
		t.Fatalf("click selected %d; want 0", h.cursor)
	}
	_, cmd = h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, Y: 3})
	if cmd == nil {
		t.Fatal("click on the selected row did not open it")
	}
	cmd()
	if !slices.Equal(opened, []string{"a"}) {
		t.Fatalf("click opened %v", opened)
	}
}

func reviewDetailServer(t *testing.T, fail bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/rest/v1/vibeflow")
		switch {
		case fail:
			w.WriteHeader(http.StatusServiceUnavailable)
		case path == "/projects/66/pr-review-summaries/job-4":
			fmt.Fprintf(w, `{"review":{"id":"job-4","project_id":66,"provider":"github","number":4,"state":"changes_requested","head_sha":%q,"rounds_started":2,"round_limit":3,"details":{"url":"https://github.com/acme/repo/pull/4","title":"Add late payment fee calculation"}},"last_completed_round":{"number":2,"head_sha":%q},"summary":"Fee rounding loses cents.\nSecond line.","finding_count":2}`, strings.Repeat("c", 40), strings.Repeat("d", 40))
		case path == "/projects/66/pr-review-summaries/job-4/findings" && r.URL.Query().Get("after_id") == "":
			fmt.Fprint(w, `{"findings":[{"id":"f1","job_id":"job-4","title":"Rounding drops cents","severity":"high","path":"billing/fee.go","line":42,"state":"present","trigger":"fee of 10.005","impact":"customers undercharged","evidence":"math.Floor on cents","verification":"go test ./billing"}],"next_after_id":"f1"}`)
		case path == "/projects/66/pr-review-summaries/job-4/findings":
			fmt.Fprint(w, `{"findings":[{"id":"f2","job_id":"job-4","title":"Missing late flag","severity":"medium","path":"billing/late.go","line":7,"state":"resolved","trigger":"t2","impact":"i2","evidence":"e2","verification":"v2"}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestReviewDetailRendersSummaryAndFindings(t *testing.T) {
	client := NewClient(reviewDetailServer(t, false).URL, "token")
	d := newReviewDetail(func() tea.Msg { return loadReviewDetail(context.Background(), client, 66, "job-4") })
	d, _ = d.update(tea.WindowSizeMsg{Width: 70, Height: 60})
	d, _ = d.update(d.load())
	view := ansi.Strip(d.render())
	t.Logf("review detail:\n%s", view)
	for _, want := range []string{
		"PR #4 · Add late payment fee calculation", "https://github.com/acme/repo/pull/4",
		"Outcome: Changes requested", "Rounds: 2 of 3 used", "Last reviewed head: ddddddd",
		"live harness transcript", "not stored",
		"Fee rounding loses cents.", "Second line.", "Findings (2)",
		"present · high · billing/fee.go:42", "Rounding drops cents", "Trigger: fee of 10.005", "Impact: customers undercharged", "Evidence: math.Floor on cents", "Verification: go test ./billing",
		"resolved · medium · billing/late.go:7", "Missing late flag",
		"q/Esc close",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail lacks %q:\n%s", want, view)
		}
	}
	checkHistoryFits(t, d.render(), 70, 60)

	// A short window scrolls; the footer stays put.
	d, _ = d.update(tea.WindowSizeMsg{Width: 70, Height: 8})
	first := ansi.Strip(d.render())
	d, _ = d.update(tea.KeyPressMsg{Code: tea.KeyDown})
	d, _ = d.update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if d.offset != 2 || ansi.Strip(d.render()) == first || !strings.Contains(ansi.Strip(d.render()), "q/Esc close") {
		t.Fatalf("detail did not scroll (offset %d):\n%s", d.offset, ansi.Strip(d.render()))
	}
	d, _ = d.update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(ansi.Strip(d.render()), "Verification: v2") {
		t.Fatalf("end did not reach the last finding:\n%s", ansi.Strip(d.render()))
	}
	checkHistoryFits(t, d.render(), 70, 8)
	if _, closed := d.update(tea.KeyPressMsg{Code: 'q', Text: "q"}); !closed {
		t.Fatal("q did not close the detail")
	}
	if _, closed := d.update(tea.KeyPressMsg{Code: tea.KeyEscape}); !closed {
		t.Fatal("Esc did not close the detail")
	}
}

func TestReviewDetailErrorIsOneLine(t *testing.T) {
	client := NewClient(reviewDetailServer(t, true).URL, "token")
	d := newReviewDetail(func() tea.Msg { return loadReviewDetail(context.Background(), client, 66, "job-4") })
	d, _ = d.update(tea.WindowSizeMsg{Width: 70, Height: 10})
	d, _ = d.update(d.load())
	view := ansi.Strip(d.render())
	if !strings.Contains(view, "Review details unavailable: review API returned HTTP 503") || strings.Count(strings.TrimSpace(view), "\n") > 9 {
		t.Fatalf("error view:\n%s", view)
	}
}
