package vibeflowcli

import (
	"os/exec"
	"slices"
	"strings"
)

// veraTmux runs a tmux command against the server of the pane it runs in.
type veraTmux func(args ...string) (string, error)

func execVeraTmux(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	return string(out), err
}

// veraBrowseSequences are the keys that browse Vera's history pane, as a
// terminal sends them, with their tmux key names.
var veraBrowseSequences = []struct{ in, key string }{
	{"\x1b[A", "Up"}, {"\x1bOA", "Up"}, {"k", "Up"},
	{"\x1b[B", "Down"}, {"\x1bOB", "Down"}, {"j", "Down"},
	{"\x1b[5~", "PPage"}, {"\x1b[6~", "NPage"},
	{"\x1b[H", "Home"}, {"\x1bOH", "Home"}, {"\x1b[1~", "Home"}, {"\x1b[7~", "Home"},
	{"\x1b[F", "End"}, {"\x1bOF", "End"}, {"\x1b[4~", "End"}, {"\x1b[8~", "End"},
	{"\r", "Enter"}, {"\n", "Enter"}, {"q", "q"},
}

// veraBrowseKeys maps keys typed in Vera's idle listener pane to tmux key
// names for its history pane. Other input is dropped.
func veraBrowseKeys(in []byte) []string {
	var keys []string
	for s := string(in); s != ""; {
		matched := false
		for _, seq := range veraBrowseSequences {
			if strings.HasPrefix(s, seq.in) {
				keys, s, matched = append(keys, seq.key), s[len(seq.in):], true
				break
			}
		}
		switch {
		case matched:
		case s == "\x1b":
			keys, s = append(keys, "Escape"), ""
		case strings.HasPrefix(s, "\x1b[") || strings.HasPrefix(s, "\x1bO"):
			// Another escape sequence: skip through its final byte.
			end := 2
			for end < len(s) && (s[end] < '@' || s[end] > '~') {
				end++
			}
			s = s[min(end+1, len(s)):]
		default:
			s = s[1:]
		}
	}
	return keys
}

// forwardVeraKeys sends the browse keys in input to the history pane beside
// the listener pane.
func forwardVeraKeys(tmux veraTmux, listener string, input []byte) {
	keys := veraBrowseKeys(input)
	if listener == "" || len(keys) == 0 {
		return
	}
	out, err := tmux("list-panes", "-t", listener, "-F", "#{pane_id}\t#{@vibeflow_vera_history}")
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if pane, tag, _ := strings.Cut(line, "\t"); tag == "1" {
			_, _ = tmux(append([]string{"send-keys", "-t", pane}, keys...)...)
			return
		}
	}
}

// veraPopupArgs opens command in a large tmux popup that closes with it.
func veraPopupArgs(command string) []string {
	return []string{"display-popup", "-E", "-w", "90%", "-h", "85%", command}
}

// veraDetailArgs turns the history pane's own command line into the one that
// shows a single review.
func veraDetailArgs(args []string, jobID string) []string {
	out := slices.Clone(args)
	if i := slices.Index(out, "--history"); i >= 0 {
		out = slices.Replace(out, i, i+1, "--review-detail", jobID)
	}
	return out
}
