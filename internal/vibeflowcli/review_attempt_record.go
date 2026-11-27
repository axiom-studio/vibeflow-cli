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
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Each review keeps a local record of its newest attempt beside the console's
// progress log, so a failure can be debugged after the harness's screen is
// gone (#5601): attempts/<job>/transcript.txt. Records are private to this
// machine (0600 files in 0700 directories), bounded, and never sent to the
// server; they may contain whatever the harness printed.
const (
	reviewAttemptKeep      = 30        // newest review records kept per console
	reviewTranscriptMax    = 512 << 10 // a transcript keeps its newest bytes
	reviewTranscriptLines  = 5000      // pane lines captured, scrollback included
	reviewTranscriptFile   = "transcript.txt"
	reviewAttemptsDirName  = "attempts"
	reviewTranscriptHeader = "Captured when the attempt ended; local to this machine.\n"
)

// reviewAttemptDir is a review's record directory, or "" when the job ID is
// not a safe single path element.
func reviewAttemptDir(logPath, jobID string) string {
	if logPath == "" || !reviewPublicID(jobID) {
		return ""
	}
	return filepath.Join(filepath.Dir(logPath), reviewAttemptsDirName, jobID)
}

// saveReviewTranscript stores text as the review's transcript, replacing the
// previous attempt's, and prunes the oldest records past reviewAttemptKeep.
func saveReviewTranscript(dir, text string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	text = strings.Join(reviewDetailText(strings.TrimRight(text, "\n")), "\n")
	if len(text) > reviewTranscriptMax {
		text = text[len(text)-reviewTranscriptMax:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:] // start on a whole line
		}
	}
	tmp := filepath.Join(dir, reviewTranscriptFile+".tmp")
	if err := os.WriteFile(tmp, []byte(reviewTranscriptHeader+text+"\n"), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, reviewTranscriptFile)); err != nil {
		return err
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now) // the record's age orders pruning
	pruneReviewAttempts(filepath.Dir(dir))
	return nil
}

// readReviewTranscript returns a review's saved transcript and when it was
// saved; ok is false when there is none.
func readReviewTranscript(dir string) (lines []string, saved time.Time, ok bool) {
	if dir == "" {
		return nil, time.Time{}, false
	}
	path := filepath.Join(dir, reviewTranscriptFile)
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	text := strings.TrimPrefix(strings.TrimRight(string(data), "\n"), reviewTranscriptHeader)
	return reviewDetailText(text), info.ModTime(), true
}

// pruneReviewAttempts keeps the newest reviewAttemptKeep review records. Only
// directories named like a job ID are touched.
func pruneReviewAttempts(attempts string) {
	entries, err := os.ReadDir(attempts)
	if err != nil {
		return
	}
	type record struct {
		name string
		mod  time.Time
	}
	var records []record
	for _, e := range entries {
		if !e.IsDir() || !reviewPublicID(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			records = append(records, record{e.Name(), info.ModTime()})
		}
	}
	if len(records) <= reviewAttemptKeep {
		return
	}
	sort.Slice(records, func(i, j int) bool { return records[i].mod.After(records[j].mod) })
	for _, r := range records[reviewAttemptKeep:] {
		_ = os.RemoveAll(filepath.Join(attempts, r.name))
	}
}
