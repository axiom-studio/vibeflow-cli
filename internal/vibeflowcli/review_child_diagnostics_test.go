//go:build darwin || linux

package vibeflowcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewChildRetainsOnlySafeFailureMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, script, category string
		exitCode, signal       int
		missing                bool
		deadline               time.Duration
	}{
		{name: "exit", script: "echo stdout-secret-canary; echo stderr-secret-canary >&2; exit 17", category: "provider_exit", exitCode: 17},
		{name: "signal", script: "kill -TERM $$", category: "provider_signal", exitCode: -1, signal: 15},
		{name: "start", missing: true, category: "provider_start_failed", exitCode: -1},
		{name: "deadline", script: "sleep 10", category: "deadline_exceeded", exitCode: -1, deadline: 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "task.txt")
			if err := os.WriteFile(input, []byte("prompt-secret-canary"), 0600); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			if tc.deadline != 0 {
				deadline = time.Now().Add(tc.deadline)
			}
			spec := reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", tc.script}, Env: []string{"PATH=/usr/bin:/bin", "MODEL_TOKEN=credential-secret-canary"}, Dir: root, InputFile: input, DeadlineAt: deadline.UnixMilli()}
			if tc.missing {
				spec.Binary = filepath.Join(root, "missing-secret-canary")
			}
			path := filepath.Join(root, "child.json")
			if err := saveReviewJSON(path, spec); err != nil {
				t.Fatal(err)
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			go func() { _, _ = writer.Write([]byte{'R'}) }()
			command := reviewChildCmd()
			command.SetContext(context.Background())
			command.SetIn(reader)
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			if err := command.RunE(command, []string{path}); err == nil {
				t.Fatal("failed provider was accepted")
			}
			data, err := os.ReadFile(filepath.Join(root, "child-diagnostic.json"))
			if err != nil {
				t.Fatalf("child exit details were discarded: %v", err)
			}
			var diagnostic struct {
				Version    int    `json:"version"`
				Category   string `json:"category"`
				ExitCode   *int   `json:"exit_code"`
				Signal     int    `json:"signal"`
				DurationMS int64  `json:"duration_ms"`
			}
			if err := json.Unmarshal(data, &diagnostic); err != nil {
				t.Fatal(err)
			}
			if diagnostic.Version != 1 || diagnostic.Category != tc.category || diagnostic.DurationMS < 0 {
				t.Fatalf("incorrect failure category: %s", data)
			}
			if tc.exitCode >= 0 && (diagnostic.ExitCode == nil || *diagnostic.ExitCode != tc.exitCode) {
				t.Fatalf("provider exit status lost: %s", data)
			}
			if tc.signal != 0 && diagnostic.Signal != tc.signal {
				t.Fatalf("provider signal lost: %s", data)
			}
			if strings.Contains(string(data), "secret-canary") || strings.Contains(string(data), root) {
				t.Fatal("diagnostic retained provider text, a prompt, a credential, or a private path")
			}
			info, err := os.Stat(filepath.Join(root, "child-diagnostic.json"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("diagnostic is not private")
			}
		})
	}
}
