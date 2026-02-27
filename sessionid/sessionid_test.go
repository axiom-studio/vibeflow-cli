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

package sessionid

import (
	"regexp"
	"testing"
)

func TestGenerateSessionID_Prefix(t *testing.T) {
	id := GenerateSessionID("/some/dir")
	if len(id) < 8 || id[:8] != "session-" {
		t.Errorf("expected prefix 'session-', got %q", id)
	}
}

func TestGenerateSessionID_Format(t *testing.T) {
	id := GenerateSessionID("")
	// Expected format: session-YYYYMMDD-HHMMSS-XXXXXXXX
	re := regexp.MustCompile(`^session-\d{8}-\d{6}-[0-9a-f]{8}$`)
	if !re.MatchString(id) {
		t.Errorf("ID %q does not match expected format session-YYYYMMDD-HHMMSS-XXXXXXXX", id)
	}
}

func TestGenerateSessionID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateSessionID("/dir")
		if seen[id] {
			t.Fatalf("duplicate ID generated: %q (iteration %d)", id, i)
		}
		seen[id] = true
	}
}

func TestGenerateSessionID_IgnoresDir(t *testing.T) {
	// The dir parameter is vestigial — verify it doesn't affect output format.
	id1 := GenerateSessionID("/path/a")
	id2 := GenerateSessionID("/path/b")
	id3 := GenerateSessionID("")

	re := regexp.MustCompile(`^session-\d{8}-\d{6}-[0-9a-f]{8}$`)
	for _, id := range []string{id1, id2, id3} {
		if !re.MatchString(id) {
			t.Errorf("ID %q has unexpected format", id)
		}
	}
}

func TestGenerateSessionID_HexSuffix(t *testing.T) {
	id := GenerateSessionID("")
	// Extract the hex suffix (last 8 characters).
	suffix := id[len(id)-8:]
	re := regexp.MustCompile(`^[0-9a-f]{8}$`)
	if !re.MatchString(suffix) {
		t.Errorf("hex suffix %q is not 8 hex characters", suffix)
	}
}
