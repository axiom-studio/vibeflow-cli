//go:build !windows

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

// TestSetProcessGroup_SetsSetpgidOnUnix verifies the Unix build of the
// platform-split detach helper (#3298) puts the child in its own process group
// so the cloud-dispatch daemon survives its parent. The Windows build is a
// documented no-op (dispatch_windows.go) so the release can cross-compile.
func TestSetProcessGroup_SetsSetpgidOnUnix(t *testing.T) {
	cmd := exec.Command("true")
	setProcessGroup(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Errorf("setProcessGroup must set Setpgid on unix; got SysProcAttr=%+v", cmd.SysProcAttr)
	}
}
