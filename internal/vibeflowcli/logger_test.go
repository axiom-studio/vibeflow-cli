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
	"strings"
	"testing"
)

func testLogger(t *testing.T) (*Logger, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	return &Logger{path: logPath, file: f}, logPath
}

func TestLogger_Info(t *testing.T) {
	l, logPath := testLogger(t)
	l.Info("test message %d", 42)
	l.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[INFO]") {
		t.Error("expected [INFO] in log")
	}
	if !strings.Contains(content, "test message 42") {
		t.Error("expected formatted message in log")
	}
}

func TestLogger_Debug(t *testing.T) {
	l, logPath := testLogger(t)
	l.Debug("debug message")
	l.Close()

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "[DEBUG]") {
		t.Error("expected [DEBUG] in log")
	}
}

func TestLogger_Warn(t *testing.T) {
	l, logPath := testLogger(t)
	l.Warn("warning: %s", "something")
	l.Close()

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "[WARN]") {
		t.Error("expected [WARN] in log")
	}
}

func TestLogger_Error(t *testing.T) {
	l, logPath := testLogger(t)
	l.Error("error: %v", "bad thing")
	l.Close()

	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "[ERROR]") {
		t.Error("expected [ERROR] in log")
	}
}

func TestLogger_Timestamp(t *testing.T) {
	l, logPath := testLogger(t)
	l.Info("ts test")
	l.Close()

	data, _ := os.ReadFile(logPath)
	content := string(data)
	// Timestamp format: YYYY-MM-DD HH:MM:SS
	if len(content) < 19 {
		t.Fatalf("log line too short: %q", content)
	}
	// Check that it starts with a date-like pattern (4 digits then dash).
	if content[4] != '-' || content[7] != '-' || content[10] != ' ' {
		t.Errorf("expected timestamp at start of log, got: %q", content[:20])
	}
}

func TestLogger_Close(t *testing.T) {
	l, _ := testLogger(t)
	l.Close()

	// After close, writing should be a no-op (not panic).
	l.Info("should be no-op")
}

func TestLogger_CloseIdempotent(t *testing.T) {
	l, _ := testLogger(t)
	l.Close()
	l.Close() // Second close should not panic.
}

func TestLogger_NilFile(t *testing.T) {
	l := &Logger{} // no file
	// Should not panic.
	l.Info("no-op message")
	l.Close()
}

func TestLogger_Rotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "rotate.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	l := &Logger{path: logPath, file: f}

	// Write enough data to exceed maxLogSize (1MB).
	msg := strings.Repeat("x", 1024)
	for i := 0; i < 1100; i++ {
		l.Info("%s", msg)
	}
	l.Close()

	// After rotation, file should be smaller than maxLogSize + some slack.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// After rotation the file is truncated and only has a rotation message
	// plus the remaining writes. It should be significantly smaller than
	// 1100 * ~1050 bytes.
	if info.Size() > maxLogSize+100*1024 {
		t.Errorf("expected file size near maxLogSize after rotation, got %d", info.Size())
	}

	// Verify rotation message is present.
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "Log rotated") {
		t.Error("expected rotation message in log")
	}
}
