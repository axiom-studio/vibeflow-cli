package vibeflowcli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
)

func TestReviewStartupAsksEveryLaunchWithoutAuthSetup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIToken = "never-display-this-token"
	for range 2 {
		m := newReviewStartupModel(context.Background(), cfg, "existing-config.yaml", reviewWatchOptions{})
		view := m.View().Content
		if !strings.Contains(view, "Launch PR review runner for this session?") || strings.Contains(view, cfg.APIToken) || strings.Contains(view, "API Token") {
			t.Fatalf("wrong session prompt: %s", view)
		}
		if m.Init() != nil {
			t.Fatal("startup performed work before consent")
		}
		next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		result := next.(reviewStartupModel)
		if !result.done || result.runner != nil || result.quit || cmd == nil {
			t.Fatal("default No should continue without a runner")
		}
	}
}

func TestReviewStartupYesAndMissingInput(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(reviewStartupModel)
	if !m.busy || cmd == nil || m.done {
		t.Fatal("Yes did not resolve runner settings")
	}
	next, _ = m.Update(reviewStartupResolvedMsg{input: &reviewStartupInput{Field: "repository", Message: "Repository checkout path:"}})
	m = next.(reviewStartupModel)
	next, _ = m.Update(tea.PasteMsg{Content: "/tmp/review checkout"})
	m = next.(reviewStartupModel)
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(reviewStartupModel)
	if m.options.Repository != "/tmp/review checkout" || !m.busy || cmd == nil {
		t.Fatal("missing checkout was not supplied")
	}
	next, _ = m.Update(reviewStartupResolvedMsg{options: m.options, input: &reviewStartupInput{Field: "repository_link", Message: "Choose link", Choices: []reviewStartupChoice{{Label: "Bitbucket", Value: "bitbucket:7"}}}})
	m = next.(reviewStartupModel)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(reviewStartupModel)
	if m.options.GitProvider != "bitbucket" || m.options.RepositoryLinkID != 7 {
		t.Fatal("provider-scoped repository link was lost")
	}
}

func TestReviewStartupFailureCanContinueWithoutRunner(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	next, _ := m.Update(reviewStartupReadyMsg{err: errors.New("selected review provider is not installed")})
	m = next.(reviewStartupModel)
	if !strings.Contains(m.View().Content, "Enter: continue without runner") {
		t.Fatal("failure has no skip action")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !next.(reviewStartupModel).done || next.(reviewStartupModel).quit {
		t.Fatal("failed runner prevented normal CLI launch")
	}
}

func TestReviewStartupProgramNo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := newReviewStartupModel(ctx, DefaultConfig(), "config.yaml", reviewWatchOptions{})
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	go p.Send(tea.KeyPressMsg{Code: 'n', Text: "n"})
	result, err := p.Run()
	if err != nil || !result.(reviewStartupModel).done || result.(reviewStartupModel).runner != nil {
		t.Fatalf("No did not finish startup: %v", err)
	}
}

func TestReviewStartupCtrlCWhileStartingQuits(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	m.busy = true
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !next.(reviewStartupModel).quit || cmd == nil {
		t.Fatal("Ctrl-C must quit even while startup is busy")
	}
}

func TestReviewStartupFitsNarrowTerminal(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	for _, width := range []int{40, 80, 120} {
		m.width, m.height = width, 24
		if got := lipgloss.Width(m.View().Content); got > width {
			t.Fatalf("prompt width %d exceeds terminal %d", got, width)
		}
	}
	m.input = &reviewStartupInput{Field: "project", Message: "Choose project", Choices: []reviewStartupChoice{}}
	for range 10 {
		m.input.Choices = append(m.input.Choices, reviewStartupChoice{Label: strings.Repeat("project", 40)})
	}
	m.width, m.height = 80, 24
	if got := lipgloss.Height(m.View().Content); got > m.height {
		t.Fatalf("long project choices overflow terminal: %d > %d", got, m.height)
	}
}

func TestReviewRunnerStatusKeepsFooterVisible(t *testing.T) {
	runner := &reviewOwnedRunner{done: make(chan struct{})}
	m := Model{config: &Config{}, hitmap: &listHitmap{}, width: 100, height: 30, reviewRunner: runner}
	for _, status := range []string{"PR review runner online", "PR review runner stopped"} {
		content := m.View().Content
		if !strings.Contains(content, status) || lipgloss.Height(content) != m.height {
			t.Fatalf("wrong runner status or layout: %s", content)
		}
		lines := strings.Split(content, "\n")
		if !strings.Contains(lines[len(lines)-1], "q: quit") {
			t.Fatal("runner status hid quit shortcut")
		}
		if status == "PR review runner online" {
			close(runner.done)
		}
	}
}
