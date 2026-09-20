//go:build darwin || linux

package vibeflowcli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewChildGuardStopsProcessGroup(t *testing.T) {
	for _, stop := range []string{"parent_pipe_closed", "deadline"} {
		t.Run(stop, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "task.txt")
			if err := os.WriteFile(input, nil, 0600); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			if stop == "deadline" {
				deadline = time.Now().Add(500 * time.Millisecond)
			}
			spec := reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", `sleep 60 & echo $! > "$1"; wait`, "review-child", filepath.Join(root, "pid")}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: root, InputFile: input, DeadlineAt: deadline.UnixMilli()}
			path := filepath.Join(root, "child.json")
			if err := saveReviewJSON(path, spec); err != nil {
				t.Fatal(err)
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			cmd := reviewChildCmd()
			cmd.SetContext(context.Background())
			cmd.SetIn(reader)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			done := make(chan error, 1)
			go func() { done <- cmd.RunE(cmd, []string{path}) }()
			if _, err := writer.Write([]byte{'R'}); err != nil {
				t.Fatal(err)
			}
			var pid int
			until := time.Now().Add(3 * time.Second)
			for time.Now().Before(until) {
				data, _ := os.ReadFile(filepath.Join(root, "pid"))
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				if pid > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid <= 0 {
				t.Fatal("real child did not start")
			}
			if stop == "parent_pipe_closed" {
				writer.Close()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("interrupted child succeeded")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("child guard did not stop")
			}
			until = time.Now().Add(3 * time.Second)
			for time.Now().Before(until) && syscall.Kill(pid, 0) == nil {
				time.Sleep(10 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				t.Fatalf("descendant %d survived %s", pid, stop)
			}
			lock, err := lockReviewFile(filepath.Join(root, "child.lock"))
			if err != nil {
				t.Fatal("guard lock remained held", err)
			}
			lock.Close()
		})
	}
}

func TestReviewChildGuardRequiresLiveSupervisor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "child.json")
	spec := reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", "exit 0"}, DeadlineAt: time.Now().Add(time.Second).UnixMilli()}
	if err := saveReviewJSON(path, spec); err != nil {
		t.Fatal(err)
	}
	cmd := reviewChildCmd()
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	if err := cmd.RunE(cmd, []string{path}); err == nil {
		t.Fatal("guard started without supervisor")
	}
}

func TestReviewProcessStopsDescendantsAfterNaturalExit(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "pid")
	cmd := exec.Command("/bin/sh", "-c", `sleep 60 & echo $! > "$1"; exit 0`, "review-process", pidPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runReviewProcess(ctx, cmd); err != nil {
		t.Fatalf("successful parent exit blocked by inherited descriptors: %v", err)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) && syscall.Kill(pid, 0) == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 || syscall.Kill(pid, 0) == nil {
		t.Fatal("background child survived natural parent exit")
	}
}

func TestReviewFetchBudgetStopsWriterAndDescendants(t *testing.T) {
	root := t.TempDir()
	watch := &reviewWatch{root: root}
	receipt := &reviewReceipt{RequestID: reviewUUID()}
	objects := filepath.Join(watch.workDir(receipt), "objects.git")
	if err := os.MkdirAll(objects, 0700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(root, "pid")
	git := filepath.Join(root, "git")
	script := "#!/bin/sh\nsleep 60 &\necho $! > " + shellQuote(pidPath) + "\ndd if=/dev/zero of=" + shellQuote(filepath.Join(objects, "incoming.pack")) + " bs=1024 count=128 2>/dev/null\nwait\n"
	if err := os.WriteFile(git, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := fetchReviewObjects(ctx, objects, "owned-fixture", strings.Repeat("a", 40), 64<<10)
	data, _ := os.ReadFile(pidPath)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid > 0 {
		defer syscall.Kill(pid, syscall.SIGKILL)
	}
	if err == nil || !strings.Contains(err.Error(), "Git objects exceed") || ctx.Err() != nil {
		t.Fatalf("growing acquisition did not stop at its budget: %v", err)
	}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) && pid > 0 && syscall.Kill(pid, 0) == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 || syscall.Kill(pid, 0) == nil {
		t.Fatalf("fetch descendant survived budget cancellation: %d", pid)
	}
	if err := watch.cleanup(receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(watch.workDir(receipt)); !os.IsNotExist(err) {
		t.Fatalf("oversized acquisition remains after cleanup: %v", err)
	}
}
