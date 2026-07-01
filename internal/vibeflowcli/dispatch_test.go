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
	"os/exec"
	"testing"
)

// TestSetProcessGroup_PresentAndSafe guards the #3297 build-tag split: the
// setProcessGroup helper must exist and be callable on every platform (Unix
// sets Setpgid, Windows is a no-op) and must not panic. Compilation of this
// test on each GOOS is itself the primary regression guard against the
// Windows-only build break (syscall.SysProcAttr.Setpgid is Unix-only).
func TestSetProcessGroup_PresentAndSafe(t *testing.T) {
	cmd := exec.Command("echo", "ok")
	setProcessGroup(cmd) // must not panic on any platform
	if cmd == nil {
		t.Fatal("cmd unexpectedly nil after setProcessGroup")
	}
}
