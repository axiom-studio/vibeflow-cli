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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A running review's popup shows what its harness is doing right now (#5599):
// the slot pane running the review is tagged with the job (reviewLiveJobOption)
// and its screen is captured as plain text. Nothing is stored; the view is live
// only while the review runs on this machine.
const (
	veraLiveRefresh  = 2 * time.Second
	veraLiveHistory  = 400 // pane lines captured, scrollback included
	veraLiveProgress = 200 // progress log lines read before filtering to the PR
	veraLiveEvents   = 200 // newest runner events shown
)

type reviewLiveMsg struct {
	running  bool      // a slot pane on this tmux server is running the review
	output   []string  // the harness's screen and scrollback, plain text
	progress []string  // this PR's lines from the console's progress log
	events   []string  // the newest attempt's runner events (#5602)
	saved    []string  // the last attempt's transcript, when not running (#5601)
	savedAt  time.Time // when that transcript was saved
}
type reviewLiveTickMsg struct{}

// loadReviewLive reads the live view of one review. run reaches the tmux
// server of the popup (Vera's server); logPath is the console's progress log.
func loadReviewLive(run veraTmux, logPath, jobID string, prNumber int) reviewLiveMsg {
	var msg reviewLiveMsg
	if prNumber > 0 {
		token := regexp.MustCompile(`#` + strconv.Itoa(prNumber) + `\b`)
		for _, line := range readReviewProgressLog(logPath, veraLiveProgress) {
			if token.MatchString(line) {
				msg.progress = append(msg.progress, strings.Join(reviewDetailText(line), " "))
			}
		}
	}
	msg.events = readReviewEvents(reviewAttemptDir(logPath, jobID), veraLiveEvents)
	pane := reviewLivePane(run, jobID)
	out, err := "", error(nil)
	if pane != "" {
		out, err = run("capture-pane", "-p", "-J", "-t", pane, "-S", "-"+strconv.Itoa(veraLiveHistory))
	}
	if pane == "" || err != nil { // not running here, or it just ended
		msg.saved, msg.savedAt, _ = readReviewTranscript(reviewAttemptDir(logPath, jobID))
		return msg
	}
	msg.running = true
	// capture-pane without -e is plain text; reviewDetailText still drops any
	// control or format rune the harness managed to leave behind.
	msg.output = reviewDetailText(strings.TrimRight(out, "\n"))
	return msg
}

// reviewLivePane finds the slot pane tagged with the job, or "".
func reviewLivePane(run veraTmux, jobID string) string {
	if run == nil || !reviewPublicID(jobID) {
		return ""
	}
	out, err := run("list-panes", "-a", "-F", "#{pane_id}\t#{"+reviewLiveJobOption+"}")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if id, job, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && job == jobID {
			return id
		}
	}
	return ""
}
