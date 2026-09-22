package vibeflowcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestReviewStartupAsksEveryLaunchWithoutAuthSetup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIToken = "never-display-this-token"
	for range 2 {
		m := newReviewStartupModel(context.Background(), cfg, "existing-config.yaml", reviewWatchOptions{})
		view := m.View().Content
		if !strings.Contains(view, "Run PR reviews while this CLI is open?") || strings.Contains(view, cfg.APIToken) || strings.Contains(view, "API Token") {
			t.Fatalf("wrong session prompt: %s", view)
		}
		if !strings.Contains(view, "> Not now") {
			t.Fatal("the visible default must decline review startup")
		}
		next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		result := next.(reviewStartupModel)
		if !result.done || result.runner != nil || result.quit || cmd == nil {
			t.Fatal("default No should continue without a runner")
		}
	}
}

func TestReviewStartupOwlAnimationKeepsControlsStill(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	m.width, m.height = 80, 24
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("owl has no animation clock")
	}
	tick := cmd()
	initial := ansi.Strip(m.View().Content)
	poses := map[int]string{0: initial}
	for frame := 1; frame <= 80; frame++ {
		next, nextTick := m.Update(tick)
		m = next.(reviewStartupModel)
		view := ansi.Strip(m.View().Content)
		poses[frame] = view
		for _, label := range []string{"Run PR reviews", "Run reviews", "> Not now", "Space:"} {
			if strings.Index(view, label) != strings.Index(initial, label) || !strings.Contains(view, label) {
				t.Fatalf("animation moved or hid %q", label)
			}
		}
		if m.busy || m.yes || m.runner != nil || nextTick == nil {
			t.Fatal("an animation tick changed consent or stopped the clock")
		}
		if lipgloss.Width(view) > 80 || lipgloss.Height(view) > 24 {
			t.Fatal("animated prompt overflows a standard terminal")
		}
	}
	for _, frame := range []int{6, 19, 48} {
		if poses[frame] == initial {
			t.Fatalf("owl did not animate its breathing, blink or tilt at frame %d", frame)
		}
	}
	if poses[80] != initial {
		t.Fatal("owl did not return to its original pose after one idle cycle")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m = next.(reviewStartupModel)
	paused := m.View().Content
	for range 25 {
		next, _ = m.Update(tick)
		m = next.(reviewStartupModel)
		if m.View().Content != paused {
			t.Fatal("Space did not pause the owl")
		}
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m = next.(reviewStartupModel)
	resumeStart := m.View().Content
	resumed := false
	for range 25 {
		next, _ = m.Update(tick)
		m = next.(reviewStartupModel)
		resumed = resumed || m.View().Content != resumeStart
	}
	if !resumed {
		t.Fatal("Space did not resume the owl")
	}
	for _, state := range []string{"busy", "input", "error", "done", "quit"} {
		t.Run(state, func(t *testing.T) {
			stopped := m
			switch state {
			case "busy":
				stopped.busy = true
			case "input":
				stopped.input = &reviewStartupInput{}
			case "error":
				stopped.err = errors.New("offline")
			case "done":
				stopped.done = true
			case "quit":
				stopped.quit = true
			}
			before := stopped.View().Content
			next, nextTick := stopped.Update(tick)
			if nextTick != nil || next.(reviewStartupModel).View().Content != before {
				t.Fatal("owl continued animating after the consent prompt")
			}
		})
	}
}

func TestReviewStartupOwlUsesTerminalColors(t *testing.T) {
	profile := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	lipgloss.SetColorProfile(termenv.TrueColor)
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	colors := regexp.MustCompile(`48;2;\d+;\d+;\d+`).FindAllString(m.View().Content, -1)
	unique := make(map[string]bool)
	for _, color := range colors {
		unique[color] = true
	}
	if len(unique) < 5 {
		t.Fatal("owl is missing its colored terminal pixels")
	}
	lipgloss.SetColorProfile(termenv.Ascii)
	if view := m.View().Content; strings.Contains(view, "\x1b[") || !strings.Contains(view, "> Not now") {
		t.Fatal("monochrome rendering must keep the prompt usable without ANSI colors")
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

func TestReviewStartupDetectedCheckoutPicker(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{Project: "12", RepositoryLinkID: 7})
	m.input = &reviewStartupInput{Field: "repository_choice", Message: "Choose a checkout for Axiom (12).", ManualMessage: "Project: Axiom (12)\nEnter a checkout matching github.com/acme/repo.", Choices: []reviewStartupChoice{
		{Label: "First checkout", Value: "/checkout/first"},
		{Label: "Second checkout", Value: "/checkout/second"},
		{Label: "Enter another path", Value: "manual"},
	}}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(reviewStartupModel)
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	chosen := next.(reviewStartupModel)
	if chosen.options.Repository != "/checkout/second" || chosen.options.RepositoryLinkID != 0 || !chosen.repositorySelected || !chosen.busy || cmd == nil || chosen.err != nil {
		t.Fatal("choosing a detected checkout did not resolve the selected path")
	}
	m.cursor = 2
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	manual := next.(reviewStartupModel)
	if cmd != nil || manual.busy || manual.err != nil || manual.input.Field != "repository" || len(manual.input.Choices) != 0 || manual.text != "" {
		t.Fatal("Enter another path must open manual input without launching a runner")
	}
	if manual.input.Message != m.input.ManualMessage {
		t.Fatal("manual correction lost the selected project and expected repository")
	}
}

func TestReviewStartupShowsAllProjects(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultProject = "vscode-vibeflow"
	for _, selection := range []string{"", "12"} {
		m := newReviewStartupModel(context.Background(), cfg, "config.yaml", reviewWatchOptions{Project: selection})
		view := m.View().Content
		if !strings.Contains(view, "All accessible projects") {
			t.Fatal("consent hides multi-project scope")
		}
	}
}

func TestReviewStartupConsentDoesNotWaitForCheckout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers["claude"] = Provider{Binary: "/bin/sh"}
	m := newReviewStartupModel(context.Background(), cfg, "config.yaml", reviewWatchOptions{Provider: "claude"})
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(reviewStartupModel)
	next, _ = m.Update(cmd())
	m = next.(reviewStartupModel)
	if !m.done || !m.enabled || m.input != nil {
		t.Fatalf("checkout blocked main UI: %+v", m)
	}
}

func TestReviewStartupCheckoutPickerPreservesExactPath(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	m.input = &reviewStartupInput{Field: "repository_choice", Choices: []reviewStartupChoice{
		{Label: "First checkout", Value: "/checkout/repo"},
		{Label: "Second checkout", Value: "/checkout/repo "},
	}}
	m.cursor = 1
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	chosen := next.(reviewStartupModel)
	if chosen.options.Repository != "/checkout/repo " || !chosen.repositorySelected || cmd == nil {
		t.Fatal("picker changed the selected checkout path")
	}
}

func TestReviewStartupCheckoutPickerFitsTerminal(t *testing.T) {
	m := newReviewStartupModel(context.Background(), DefaultConfig(), "config.yaml", reviewWatchOptions{})
	m.input = &reviewStartupInput{Field: "repository_choice", Message: "Project: Axiom\nRepositories: " + strings.Repeat("long-repository-name ", 30)}
	for i := range 10 {
		m.input.Choices = append(m.input.Choices, reviewStartupChoice{Label: strings.Repeat("checkout ", 20), Value: fmt.Sprintf("/a/long/path/to/checkout-%d", i)})
	}
	m.cursor = 7
	for _, size := range [][2]int{{40, 16}, {40, 18}, {40, 24}, {80, 16}, {80, 18}, {80, 24}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height || !strings.Contains(view, "Enter: continue") || !strings.Contains(view, "checkout-7") {
			t.Fatalf("checkout picker hid the selected path or controls at %dx%d", m.width, m.height)
		}
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
	for _, size := range [][2]int{{40, 24}, {60, 20}, {80, 16}, {80, 24}, {120, 30}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		if got := lipgloss.Width(view); got > m.width {
			t.Fatalf("prompt width %d exceeds terminal %d", got, m.width)
		}
		if got := lipgloss.Height(view); got > m.height {
			t.Fatalf("prompt height %d exceeds terminal %d", got, m.height)
		}
		if !strings.Contains(view, "> Not now") || !strings.Contains(view, "Ctrl+C:") {
			t.Fatal("artwork hid consent or exit controls")
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
	m := Model{config: &Config{}, hitmap: &listHitmap{}, width: 100, height: 30}
	for _, state := range []string{"online", "failed"} {
		m.reviewStatuses = []reviewRunnerStatus{{State: state}}
		content := m.View().Content
		if !strings.Contains(content, "PR review runners:") || lipgloss.Height(content) != m.height {
			t.Fatalf("wrong runner status or layout: %s", content)
		}
		lines := strings.Split(content, "\n")
		if !strings.Contains(lines[len(lines)-1], "q: quit") {
			t.Fatal("runner status hid quit shortcut")
		}
	}
}
