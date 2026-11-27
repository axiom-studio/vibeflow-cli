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
	"context"
	"regexp"
	"strings"
	"time"
)

// An interactive harness that hits a fatal login or access error stops at its
// prompt and would hold the review until the lease ran out, recorded only as
// "lease_rejected" (#7691; PR #49 sat 13 minutes on "Please run /login · API
// Error: 403"). The listener watches its own pane and fails the attempt early
// with the real cause, but only when the screen has not changed for
// reviewStuckAfter AND its last lines carry a harness error line: a running
// review's screen shows diffs and code, so a bare "403" proves nothing.
//
// reviewStuckAfter stays under reviewWaitNotice (60 s): Vera's own "still
// waiting" notice changes the screen and would restart the count.
var (
	reviewStuckEvery = 5 * time.Second
	reviewStuckAfter = 45 * time.Second
)

// reviewStuckLines is how many of the screen's last non-empty lines are read.
const reviewStuckLines = 25

var reviewStuckMarkers = []struct {
	re       *regexp.Regexp
	category string
}{
	{regexp.MustCompile(`please run /login`), "authentication_required"},
	{regexp.MustCompile(`\bapi error: 401\b`), "authentication_required"},
	{regexp.MustCompile(`\bapi error: 403\b`), "access_denied"},
	{regexp.MustCompile(`\bunexpected status 401\b`), "authentication_required"},
	{regexp.MustCompile(`\bunexpected status 403\b`), "access_denied"},
}

// reviewHarnessStuckError cancels an attempt whose harness sits at a fatal
// error screen; its category becomes the attempt's failure category.
type reviewHarnessStuckError struct{ category string }

func (e *reviewHarnessStuckError) Error() string {
	return "review harness stopped at an error screen (" + e.category + ")"
}

// classifyStuckHarnessScreen names the fatal error a harness screen ends on,
// or "" when its last lines show none.
func classifyStuckHarnessScreen(screen string) string {
	var tail []string
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0 && len(tail) < reviewStuckLines; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			tail = append(tail, strings.ToLower(line))
		}
	}
	text := strings.Join(tail, "\n")
	for _, m := range reviewStuckMarkers {
		if m.re.MatchString(text) {
			return m.category
		}
	}
	return ""
}

// watchStuckHarness samples pane every interval until ctx ends, and calls
// stuck once when the screen has not changed for after and ends on a fatal
// harness error.
func watchStuckHarness(ctx context.Context, run veraTmux, pane string, every, after time.Duration, stuck func(category string)) {
	if run == nil || pane == "" {
		return
	}
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		last, changed := "", time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				screen, err := run("capture-pane", "-p", "-J", "-t", pane)
				if err != nil {
					continue
				}
				if screen != last {
					last, changed = screen, now
					continue
				}
				if now.Sub(changed) < after {
					continue
				}
				if category := classifyStuckHarnessScreen(screen); category != "" {
					stuck(category)
					return
				}
			}
		}
	}()
}
