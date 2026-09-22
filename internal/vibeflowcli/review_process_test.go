//go:build darwin || linux

package vibeflowcli

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestReviewCapacityGuardRetainsSlotAndQuarantinesSIGKILL(t *testing.T) {
	root := t.TempDir()
	c, err := newReviewCapacity(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	dir := filepath.Join(root, "review-runners", strings.Repeat("a", 32))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := &reviewReceipt{JobID: "job", RequestID: reviewUUID()}
	w := &reviewWatch{root: dir, capacity: c, state: reviewRunnerState{Pending: p}}
	if ok, err := w.acquireCapacity(); err != nil || !ok {
		t.Fatal(err)
	}
	work := w.workDir(p)
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(work, "input.txt")
	if err := os.WriteFile(input, nil, 0600); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(work, "pid")
	spec := reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", `[ ! -e /dev/fd/3 ] || exit 77; sleep 60 & echo "$!" > "$1"; wait`, "fixture", pidPath}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: work, InputFile: input, DeadlineAt: time.Now().Add(time.Minute).UnixMilli(), CapacityFD: 3, Cleanup: &reviewProviderCleanup{Reservation: *p.Capacity, RequestID: p.RequestID, JobID: p.JobID, AttemptID: "attempt"}}
	path := filepath.Join(work, "child.json")
	if err := saveReviewJSON(path, spec); err != nil {
		t.Fatal(err)
	}
	guard := exec.Command(os.Args[0], "review-child", path)
	guard.ExtraFiles = []*os.File{w.slot}
	pipe, err := guard.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	if err := guard.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { guard.Process.Kill(); guard.Wait() }()
	if _, err := pipe.Write([]byte{'R'}); err != nil {
		t.Fatal(err)
	}
	var pid int
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		data, _ := os.ReadFile(pidPath)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("model did not start with capacity descriptor closed")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	w.releaseCapacity()
	if slot, err := lockReviewFile(filepath.Join(c.Directory, p.Capacity.Slot)); err == nil {
		slot.Close()
		t.Fatal("guard did not retain duplicate slot ownership")
	}
	marker := filepath.Join(work, "provider-cleanup-pending.json")
	data, err := os.ReadFile(marker)
	var cleanup reviewProviderCleanup
	if err != nil || json.Unmarshal(data, &cleanup) != nil || cleanup.RequestID != p.RequestID {
		t.Fatal("guard launched without durable cleanup marker", err)
	}
	if err := guard.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	guard.Wait()
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("fixture needs a surviving descendant after guard SIGKILL")
	}
	if err := w.cleanup(p); err == nil {
		t.Fatal("guard death was mistaken for verified cleanup")
	}
	c.owner.Close()
	next, err := newReviewCapacity(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	first, err := next.tryAcquire()
	if err != nil || first == nil {
		t.Fatal("cleanup debt blocked healthy remaining capacity", err)
	}
	defer first.Close()
	if second, err := next.tryAcquire(); err != nil || second != nil {
		if second != nil {
			second.Close()
		}
		t.Fatal("restart silently restored unverified slot", err)
	}
}

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
	if pid <= 0 || syscall.Kill(pid, 0) == nil {
		t.Fatal("provider returned before background child termination was confirmed")
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
