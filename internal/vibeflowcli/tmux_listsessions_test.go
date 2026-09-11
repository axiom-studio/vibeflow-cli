/*
 * Copyright (c) 2026. AXIOM STUDIO AI Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package vibeflowcli

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// TestTmuxListDelim_PrintableCollisionSafe is the fail-without-fix regression
// guard for issue #3490: the ListSessions field delimiter must be a printable,
// non-whitespace sentinel. tmux sanitizes control characters (including TAB,
// 0x09) to '_' in list-sessions -F output when $TMUX is unset — i.e. when
// vibeflow runs from a plain shell — which collapsed every row into a single
// field and silently emptied the session list. Reverting the delimiter to a
// control character (the original bug) must fail this test. It is
// version-independent, so it guards the invariant even on tmux builds that do
// not reproduce the mangling.
func TestTmuxListDelim_PrintableCollisionSafe(t *testing.T) {
	if tmuxListDelim == "" {
		t.Fatal("tmuxListDelim is empty")
	}
	for _, r := range tmuxListDelim {
		if r == '\t' {
			t.Fatalf("tmuxListDelim is/contains TAB %q — the exact control char #3490 breaks on", r)
		}
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			t.Fatalf("tmuxListDelim contains non-printable or whitespace rune %q; tmux mangles such chars outside a client", r)
		}
	}
	// A ':'-based sentinel is collision-proof for the split-critical
	// session_name field because tmux forbids ':' in session names.
	if !strings.Contains(tmuxListDelim, ":") {
		t.Errorf("tmuxListDelim = %q; want a ':'-based sentinel (tmux forbids ':' in session names, so it cannot collide with a name)", tmuxListDelim)
	}
	// The -F format must use the delimiter for all six fields (five separators)
	// and must not carry a stray TAB.
	if n := strings.Count(listSessionsFormat, tmuxListDelim); n != 5 {
		t.Errorf("listSessionsFormat has %d delimiters, want 5 (six fields): %q", n, listSessionsFormat)
	}
	if strings.Contains(listSessionsFormat, "\t") {
		t.Errorf("listSessionsFormat still contains a TAB: %q", listSessionsFormat)
	}
}

func TestParseTmuxSessionLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []TmuxSession
	}{
		{
			name: "single session, colons in the created-time string do not fool the ::: split",
			in:   "vibeflow_claude-session-2c5a41db:::$3:::1:::0:::Wed Jul  9 04:15:56 2026:::0",
			want: []TmuxSession{{
				Name: "vibeflow_claude-session-2c5a41db", ID: "$3",
				Windows: 1, Attached: false, PaneDead: false,
				CreatedAt: "Wed Jul  9 04:15:56 2026",
			}},
		},
		{
			name: "attached, multiple windows, dead pane",
			in:   "vibeflow_codex-x:::$7:::3:::1:::created:::1",
			want: []TmuxSession{{
				Name: "vibeflow_codex-x", ID: "$7",
				Windows: 3, Attached: true, PaneDead: true, CreatedAt: "created",
			}},
		},
		{
			name: "empty created-time field",
			in:   "vibeflow_p:::$1:::1:::0::::::0",
			want: []TmuxSession{{
				Name: "vibeflow_p", ID: "$1",
				Windows: 1, Attached: false, PaneDead: false, CreatedAt: "",
			}},
		},
		{
			name: "five fields, pane_dead absent, defaults to not dead",
			in:   "vibeflow_q:::$2:::2:::0:::created",
			want: []TmuxSession{{
				Name: "vibeflow_q", ID: "$2",
				Windows: 2, Attached: false, PaneDead: false, CreatedAt: "created",
			}},
		},
		{
			name: "non-vibeflow prefix is skipped",
			in:   "other_session:::$4:::1:::0:::c:::0",
			want: nil,
		},
		{
			// Reproduces the #3490 failure mode at the parse layer: on a tmux
			// build that mangles control chars outside a client, the old
			// TAB-delimited row arrived with every TAB replaced by '_', so the
			// whole line was a single field with no ':::' — correctly skipped
			// (len(parts) < 5). The new ':::' format is immune because colons
			// are not sanitized.
			name: "fully-mangled single-field line (old TAB output) is skipped",
			in:   "vibeflow_x_$0_1_0__0",
			want: nil,
		},
		{
			name: "blank and whitespace-only input yields no sessions",
			in:   "\n  \n",
			want: nil,
		},
		{
			name: "empty input yields no sessions",
			in:   "",
			want: nil,
		},
		{
			name: "multiple sessions with a non-vibeflow row filtered out",
			in: strings.Join([]string{
				"vibeflow_a:::$1:::1:::0:::c:::0",
				"unrelated:::$2:::1:::0:::c:::0",
				"vibeflow_b:::$3:::2:::1:::c:::1",
			}, "\n"),
			want: []TmuxSession{
				{Name: "vibeflow_a", ID: "$1", Windows: 1, Attached: false, PaneDead: false, CreatedAt: "c"},
				{Name: "vibeflow_b", ID: "$3", Windows: 2, Attached: true, PaneDead: true, CreatedAt: "c"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTmuxSessionLines(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseTmuxSessionLines(%q)\n got = %+v\nwant = %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestListSessions_RealTmux exercises ListSessions end-to-end against a live
// tmux server on an isolated socket, with $TMUX unset to mirror the plain-shell
// invocation from #3490. On tmux builds that sanitize control chars outside a
// client (the reporter's environment) this is a true regression guard; on
// builds that do not (e.g. tmux 3.6a) it still asserts the ':::' format lists
// real sessions and honors the vibeflow_ prefix filter. No mocks — real tmux.
func TestListSessions_RealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Reproduce the plain-shell condition: tmux mangles control chars in -F
	// output only when $TMUX is unset. Restore whatever was there afterwards.
	if orig, ok := os.LookupEnv("TMUX"); ok {
		os.Unsetenv("TMUX")
		t.Cleanup(func() { os.Setenv("TMUX", orig) })
	}

	tm := NewTmuxManager("vftest-listsessions")
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()

	if _, err := tm.run("new-session", "-d", "-s", "vibeflow_listtest"); err != nil {
		t.Skipf("cannot create tmux session: %v", err)
	}
	// A non-vibeflow session must be excluded by the prefix filter.
	if _, err := tm.run("new-session", "-d", "-s", "unrelated_listtest"); err != nil {
		t.Skipf("cannot create second tmux session: %v", err)
	}

	sessions, err := tm.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}

	var found *TmuxSession
	for i := range sessions {
		switch sessions[i].Name {
		case "vibeflow_listtest":
			found = &sessions[i]
		case "unrelated_listtest":
			t.Errorf("ListSessions returned non-vibeflow session %q; prefix filter failed", sessions[i].Name)
		}
	}
	if found == nil {
		t.Fatalf("ListSessions() did not return vibeflow_listtest; got %+v", sessions)
	}
	if found.Windows < 1 {
		t.Errorf("Windows = %d, want >= 1", found.Windows)
	}
	if found.Attached {
		t.Errorf("Attached = true, want false for a detached session")
	}
	if found.PaneDead {
		t.Errorf("PaneDead = true, want false for a live session")
	}
	if found.ID == "" {
		t.Errorf("ID is empty, want a tmux session id like $N")
	}
}
