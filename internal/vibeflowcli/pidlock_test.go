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
	"strconv"
	"testing"
)

func TestPIDLockPath(t *testing.T) {
	path := PIDLockPath()
	if path == "" {
		t.Error("expected non-empty path")
	}
	if filepath.Base(path) != "vibeflow.pid" {
		t.Errorf("expected vibeflow.pid, got %q", filepath.Base(path))
	}
}

func TestReadPIDLock_MissingFile(t *testing.T) {
	pid, alive := readPIDLock(filepath.Join(t.TempDir(), "nope.pid"))
	if alive {
		t.Error("expected not alive for missing file")
	}
	if pid != 0 {
		t.Errorf("expected pid 0, got %d", pid)
	}
}

func TestReadPIDLock_InvalidContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pid")
	os.WriteFile(path, []byte("not-a-number"), 0600)

	pid, alive := readPIDLock(path)
	if alive {
		t.Error("expected not alive for invalid PID content")
	}
	if pid != 0 {
		t.Errorf("expected pid 0, got %d", pid)
	}
}

func TestReadPIDLock_ZeroPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zero.pid")
	os.WriteFile(path, []byte("0"), 0600)

	pid, alive := readPIDLock(path)
	if alive {
		t.Error("expected not alive for PID 0")
	}
	if pid != 0 {
		t.Errorf("expected pid 0, got %d", pid)
	}
}

func TestReadPIDLock_NegativePID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "neg.pid")
	os.WriteFile(path, []byte("-1"), 0600)

	pid, alive := readPIDLock(path)
	if alive {
		t.Error("expected not alive for negative PID")
	}
	if pid != 0 {
		t.Errorf("expected pid 0, got %d", pid)
	}
}

func TestReadPIDLock_CurrentProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current.pid")
	os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)

	pid, alive := readPIDLock(path)
	if !alive {
		t.Error("expected alive for current process PID")
	}
	if pid != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), pid)
	}
}

func TestReadPIDLock_DeadProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dead.pid")
	// Use a very high PID that's almost certainly not running.
	os.WriteFile(path, []byte("4999999"), 0600)

	_, alive := readPIDLock(path)
	if alive {
		t.Error("expected not alive for dead process PID")
	}
}

func TestReadPIDLock_WhitespaceContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.pid")
	os.WriteFile(path, []byte("  "+strconv.Itoa(os.Getpid())+"  \n"), 0600)

	pid, alive := readPIDLock(path)
	if !alive {
		t.Error("expected alive (whitespace trimmed)")
	}
	if pid != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), pid)
	}
}

func TestProcessAlive_CurrentProcess(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
}

func TestProcessAlive_DeadProcess(t *testing.T) {
	// PID 4999999 almost certainly doesn't exist.
	if processAlive(4999999) {
		t.Error("PID 4999999 should not be alive")
	}
}
