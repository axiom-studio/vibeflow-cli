package vibeflowcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// A Vera console splits one repository's listener into slots. In the TUI each
// slot runs the ordinary interactive listener in its own tmux window, so a
// running job keeps the harness's live session; the first window shows this
// dashboard. Window names follow the job: "answer #34" while working, the
// slot label ("answer 1") while listening.

type reviewSlotSpec struct {
	Kind, Label string
}

// reviewSlotList is the one place slot names come from: the TUI windows, the
// dashboard, the session row and in-process slots must agree on them.
func reviewSlotList(answerSlots, reviewSlots int) []reviewSlotSpec {
	var slots []reviewSlotSpec
	for i := 1; i <= answerSlots; i++ {
		slots = append(slots, reviewSlotSpec{"question", fmt.Sprintf("answer %d", i)})
	}
	for i := 1; i <= reviewSlots; i++ {
		slots = append(slots, reviewSlotSpec{"review", fmt.Sprintf("review %d", i)})
	}
	return slots
}

// reviewSlotOptions are the runner options of one slot: its own name, so its
// own state file, lock and server registration.
func reviewSlotOptions(o reviewWatchOptions, slot reviewSlotSpec) reviewWatchOptions {
	o.SlotKind, o.SlotLabel, o.Name = slot.Kind, slot.Label, o.Name+" · "+slot.Label
	return o
}

// veraJobWindowName names a slot's window after its job.
func veraJobWindowName(p *reviewReceipt) string {
	if p == nil || p.Execution == nil {
		return ""
	}
	kind := "review"
	if p.Execution.isQuestion() {
		kind = "answer"
	}
	return kind + " " + strings.TrimPrefix(reviewPRLabel(p.Execution.Review), "PR ")
}

// nameWindow renames this slot's tmux window; a listener outside a console
// slot, or outside tmux, never renames anything.
func (w *reviewWatch) nameWindow(name string) {
	pane := os.Getenv("TMUX_PANE")
	if w.options.SlotLabel == "" || pane == "" || name == "" {
		return
	}
	_, _ = execVeraTmux("rename-window", "-t", pane, name)
}

// veraSlotActivity describes one slot from its durable receipt.
type veraSlotActivity struct {
	Slot    reviewSlotSpec
	Busy    bool
	Kind    string // "review" or "answer" when busy
	PR      string
	Elapsed time.Duration
}

func veraSlotActivities(serverURL string, o reviewWatchOptions, answerSlots, reviewSlots int, now time.Time) []veraSlotActivity {
	var out []veraSlotActivity
	for _, slot := range reviewSlotList(answerSlots, reviewSlots) {
		a := veraSlotActivity{Slot: slot}
		if p := veraPendingReceipt(serverURL, reviewSlotOptions(o, slot)); p != nil {
			a.Busy = true
			if p.Execution != nil {
				a.Kind, a.PR = "review", reviewPRLabel(p.Execution.Review)
				if p.Execution.isQuestion() {
					a.Kind = "answer"
				}
				if p.ClaimedAt > 0 {
					a.Elapsed = now.Sub(time.UnixMilli(p.ClaimedAt)).Truncate(time.Second)
				}
			}
		}
		out = append(out, a)
	}
	return out
}

// veraSlotsRowStatus is the TUI session row for a Vera console.
func veraSlotsRowStatus(serverURL string, o reviewWatchOptions, answerSlots, reviewSlots int) (status, work string) {
	var busy []string
	for _, a := range veraSlotActivities(serverURL, o, answerSlots, reviewSlots, time.Now()) {
		switch {
		case a.Busy && a.PR != "" && a.Kind == "answer":
			busy = append(busy, "answering on "+a.PR)
		case a.Busy && a.PR != "":
			busy = append(busy, "reviewing "+a.PR)
		case a.Busy:
			busy = append(busy, "claiming")
		}
	}
	if len(busy) == 0 {
		return "listening", "listening"
	}
	return "reviewing", strings.Join(busy, " · ")
}

// renderVeraDashboard draws the console's first window.
func renderVeraDashboard(w io.Writer, o reviewWatchOptions, activities []veraSlotActivity) {
	used := map[string]int{}
	total := map[string]int{}
	for _, a := range activities {
		total[a.Slot.Kind]++
		if a.Busy {
			used[a.Slot.Kind]++
		}
	}
	fmt.Fprint(w, "\x1b[H\x1b[2J")
	fmt.Fprintf(w, "Vera · %s    Answers %d/%d · Reviews %d/%d\n", o.Name, used["question"], total["question"], used["review"], total["review"])
	fmt.Fprintln(w, strings.Repeat("─", 72))
	for _, a := range activities {
		line := "listening"
		switch {
		case a.Busy && a.PR != "" && a.Kind == "answer":
			line = "Answering a question on " + a.PR
		case a.Busy && a.PR != "":
			line = "Reviewing " + a.PR
		case a.Busy:
			line = "claiming work"
		}
		elapsed := ""
		if a.Elapsed > 0 {
			elapsed = a.Elapsed.String()
		}
		fmt.Fprintf(w, "  %-10s %-44s %s\n", a.Slot.Label, line, elapsed)
	}
	fmt.Fprintln(w, strings.Repeat("─", 72))
	fmt.Fprintln(w, "Each slot runs in its own tmux window, named after its job while it works.")
	fmt.Fprintln(w, "Ctrl-b w picks a window · Ctrl-b n / p moves between them · the history pane is on the right.")
}

// runVeraDashboard redraws the dashboard every two seconds from the slots'
// receipts. It never claims work; the slot windows do.
func runVeraDashboard(ctx context.Context, cfg *Config, o reviewWatchOptions, answerSlots, reviewSlots int, out io.Writer) error {
	if answerSlots+reviewSlots == 0 {
		return fmt.Errorf("the Vera dashboard needs --answer-slots or --review-slots")
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		renderVeraDashboard(out, o, veraSlotActivities(cfg.ServerURL, o, answerSlots, reviewSlots, time.Now()))
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
