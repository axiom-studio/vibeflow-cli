//go:build darwin || linux

package vibeflowcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReviewCapacityRechecksMarkerUnderOwnership(t *testing.T) {
	for _, target := range []string{"old-slot", "child-lock"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			capacity, err := newReviewCapacity(root, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer capacity.Close()
			dir := filepath.Join(root, "review-runners", strings.Repeat("a", 32))
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			p := &reviewReceipt{JobID: "job", RequestID: reviewUUID()}
			w := &reviewWatch{root: dir, capacity: capacity, state: reviewRunnerState{Pending: p}}
			if ok, err := w.acquireCapacity(); err != nil || !ok {
				t.Fatal(err)
			}
			work := w.workDir(p)
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(work, "input.fifo")
			if err := syscall.Mkfifo(input, 0600); err != nil {
				t.Fatal(err)
			}
			spec := reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", `echo $$; exec sleep 60`}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: work, InputFile: input, DeadlineAt: time.Now().Add(time.Minute).UnixMilli(), CapacityFD: 3, Cleanup: &reviewProviderCleanup{Reservation: *p.Capacity, RequestID: p.RequestID, JobID: p.JobID, AttemptID: "attempt"}}
			path := filepath.Join(work, "child.json")
			if err := saveReviewJSON(path, spec); err != nil {
				t.Fatal(err)
			}
			guard := exec.Command(os.Args[0], "review-child", path)
			guard.ExtraFiles = []*os.File{w.slot}
			control, err := guard.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			output, err := guard.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := guard.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { guard.Process.Kill(); guard.Wait() }()
			w.releaseCapacity()
			if _, err := control.Write([]byte{'R'}); err != nil {
				t.Fatal(err)
			}
			// The real guard cannot publish its marker until its FIFO input opens.
			// This establishes the exact stale-check boundary without a sleep.
			if err := w.providerCleanupPending(p); err != nil {
				t.Fatal("initial marker check", err)
			}
			opened := make(chan error, 1)
			go func() {
				f, err := os.OpenFile(input, os.O_WRONLY, 0)
				if err == nil {
					err = f.Close()
				}
				opened <- err
			}()
			select {
			case err := <-opened:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("guard did not reach input barrier")
			}
			ready := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
			var pid int
			select {
			case line := <-ready:
				pid, _ = strconv.Atoi(strings.TrimSpace(line))
			case <-time.After(5 * time.Second):
				t.Fatal("provider did not report readiness")
			}
			if pid <= 0 {
				t.Fatal("invalid provider readiness")
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			if err := guard.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			guard.Wait()
			if syscall.Kill(pid, 0) != nil {
				t.Fatal("fixture requires a surviving provider")
			}
			lockPath := filepath.Join(capacity.Directory, p.Capacity.Slot)
			if target == "child-lock" {
				lockPath = filepath.Join(work, "child.lock")
			}
			lock, err := w.lockReviewCleanup(lockPath, p)
			if lock != nil {
				lock.Close()
			}
			if !errors.Is(err, errReviewCleanupUnverified) {
				t.Fatalf("guard published marker after precheck, but recovery accepted ownership: %v", err)
			}
			if err := w.cleanup(p); !errors.Is(err, errReviewCleanupUnverified) {
				t.Fatal("standalone cleanup lost uncertainty", err)
			}
			if _, err := reviewCapacityReceipts(root); err != nil {
				t.Fatal("recovery corrupted reservation/marker association", err)
			}
		})
	}
}

func TestReviewCapacityAdmissionBeforeClaim(t *testing.T) {
	root := t.TempDir()
	c, err := newReviewCapacity(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	held, err := c.tryAcquire()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	claims, beats := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			beats++
			out.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/work"):
			fmt.Fprint(out, `{"reviews":[{"id":"job","repository_link_id":7,"provider":"github"}],"next_after_id":"next"}`)
		case strings.HasSuffix(r.URL.Path, "/claim"):
			claims++
			out.WriteHeader(409)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			out.WriteHeader(400)
		}
	}))
	defer server.Close()
	dir := filepath.Join(root, "review-runners", strings.Repeat("a", 32))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	w := &reviewWatch{client: NewClient(server.URL, "token"), root: dir, capacity: c, providerReady: true, output: io.Discard, options: reviewWatchOptions{ProjectID: 1, RepositoryLinkID: 7, GitProvider: "github"}}
	if err := w.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if claims != 0 || beats != 1 || w.state.Cursor != "" || w.state.Pending != nil {
		t.Fatalf("saturation consumed work: claims=%d heartbeat=%d state=%+v", claims, beats, w.state)
	}
	held.Close()
	if err := w.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if claims != 1 || w.state.Pending != nil {
		t.Fatal("rejected claim retained reservation")
	}
	slot, err := c.tryAcquire()
	if err != nil || slot == nil {
		t.Fatal("rejected claim leaked capacity", err)
	}
	slot.Close()
}

func TestReviewCapacityOrphanPendingAndCleanupDebt(t *testing.T) {
	root := t.TempDir()
	old, err := newReviewCapacity(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "review-runners", strings.Repeat("a", 32))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := &reviewReceipt{JobID: "job", RequestID: reviewUUID(), Result: json.RawMessage(`{"saved":true}`)}
	w := &reviewWatch{root: dir, capacity: old, state: reviewRunnerState{Pending: p}}
	if ok, err := w.acquireCapacity(); err != nil || !ok {
		t.Fatal("initial reservation", err)
	}
	w.slot.Close()
	// A live independent TUI is outside the new group's limit.
	next, err := newReviewCapacity(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	a, err := next.tryAcquire()
	if err != nil || a == nil {
		t.Fatal("live group enrolled in another group's limit", err)
	}
	a.Close()
	old.owner.Close()
	if a, err := next.tryAcquire(); err != nil || a != nil {
		if a != nil {
			a.Close()
		}
		t.Fatal("orphan pending result lost its reservation", err)
	}
	w.slot = nil
	w.capacity = next
	if ok, err := w.acquireCapacity(); err != nil || !ok {
		t.Fatal("recovery double-counted own debt", err)
	}
	marker := filepath.Join(w.workDir(p), "provider-cleanup-pending.json")
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveReviewJSON(marker, reviewProviderCleanup{Reservation: *p.Capacity, RequestID: p.RequestID, JobID: p.JobID, AttemptID: "attempt"}); err != nil {
		t.Fatal(err)
	}
	w.slot.Close()
	w.slot = nil
	if err := w.cleanup(p); err == nil || !strings.Contains(err.Error(), "Provider cleanup unverified") {
		t.Fatal("free lock falsely confirmed provider cleanup", err)
	}
	if a, err := next.tryAcquire(); err != nil || a != nil {
		if a != nil {
			a.Close()
		}
		t.Fatal("unresolved marker admitted another review", err)
	}
	if err := next.Close(); err == nil {
		t.Fatal("removed group containing cleanup debt")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := w.finishReceipt(p); err != nil {
		t.Fatal(err)
	}
	a, err = next.tryAcquire()
	if err != nil || a == nil {
		t.Fatal("verified completed receipt still reserved", err)
	}
	a.Close()
}

func TestReviewCapacityLimits(t *testing.T) {
	c, err := newReviewCapacity(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.tryAcquire()
	if err != nil || a == nil {
		t.Fatal("first slot", err)
	}
	defer a.Close()
	b, err := c.tryAcquire()
	if err != nil || b == nil {
		t.Fatal("second slot", err)
	}
	defer b.Close()
	if extra, err := c.tryAcquire(); err != nil || extra != nil {
		if extra != nil {
			extra.Close()
		}
		t.Fatal("capacity exceeded", err)
	}
	if err := c.Close(); err == nil {
		t.Fatal("removed capacity with held slots")
	}
	b.Close()
	replacement, err := c.tryAcquire()
	if err != nil || replacement == nil {
		t.Fatal("slot not reusable", err)
	}
	replacement.Close()
	a.Close()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReviewCapacityLazySlotsAndFilesystemErrors(t *testing.T) {
	c, err := newReviewCapacity(t.TempDir(), 1000000)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(c.Directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("eager slot allocation: %d %v", len(entries), err)
	}
	a, err := c.tryAcquire()
	if err != nil || a == nil {
		t.Fatal(err)
	}
	a.Close()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if slot, err := c.tryAcquire(); err == nil || slot != nil {
		t.Fatal("missing directory reported as saturation")
	}
}

func TestReviewCapacityDebtAppearingWhileSlotHeld(t *testing.T) {
	root := t.TempDir()
	old, _ := newReviewCapacity(root, 1)
	defer old.Close()
	dir := filepath.Join(root, "review-runners", strings.Repeat("b", 32))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	w := &reviewWatch{root: dir, capacity: old, state: reviewRunnerState{Pending: &reviewReceipt{JobID: "job", RequestID: reviewUUID()}}}
	if ok, err := w.acquireCapacity(); err != nil || !ok {
		t.Fatal(err)
	}
	defer w.releaseCapacity()
	next, _ := newReviewCapacity(root, 2)
	defer next.Close()
	first, err := next.tryAcquire()
	if err != nil || first == nil {
		t.Fatal(err)
	}
	defer first.Close()
	old.owner.Close()
	if second, err := next.tryAcquire(); err != nil || second != nil {
		if second != nil {
			second.Close()
		}
		t.Fatal("new debt exceeded active capacity", err)
	}
}

func TestReviewCapacityLowerLimitCanDrainExistingReceipts(t *testing.T) {
	root := t.TempDir()
	old, _ := newReviewCapacity(root, 2)
	defer old.Close()
	var watchers []*reviewWatch
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(root, "review-runners", strings.Repeat(name, 32))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		w := &reviewWatch{root: dir, capacity: old, state: reviewRunnerState{Pending: &reviewReceipt{JobID: name, RequestID: reviewUUID(), Result: json.RawMessage(`{"saved":true}`)}}}
		if ok, err := w.acquireCapacity(); err != nil || !ok {
			t.Fatal(err)
		}
		w.releaseCapacity()
		watchers = append(watchers, w)
	}
	old.owner.Close()
	next, _ := newReviewCapacity(root, 1)
	defer next.Close()
	// A lost claim response can still launch a provider, so it cannot use the
	// saved-result drain exception while other debt consumes the lowered limit.
	watchers[0].capacity = next
	watchers[0].state.Pending.Result = nil
	if err := watchers[0].save(); err != nil {
		t.Fatal(err)
	}
	if ok, err := watchers[0].acquireCapacity(); err != nil || ok {
		t.Fatal("lost claim bypassed lower execution limit", err)
	}
	watchers[0].state.Pending.Result = json.RawMessage(`{"saved":true}`)
	if err := watchers[0].save(); err != nil {
		t.Fatal(err)
	}
	for _, w := range watchers {
		w.capacity = next
		if ok, err := w.acquireCapacity(); err != nil || !ok {
			t.Fatal("lowered limit deadlocked existing result recovery", err)
		}
		if err := w.finishReceipt(w.state.Pending); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReviewCapacityOrphanMarkerWithoutPendingReceipt(t *testing.T) {
	root := t.TempDir()
	old, _ := newReviewCapacity(root, 1)
	defer old.Close()
	request := reviewUUID()
	dir := filepath.Join(root, "review-runners", strings.Repeat("a", 32), "work", request)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "provider-cleanup-pending.json")
	if err := saveReviewJSON(marker, reviewProviderCleanup{Reservation: reviewReservation{Directory: old.Directory, Slot: "slot-0"}, RequestID: request, JobID: "job", AttemptID: "attempt"}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err == nil {
		t.Fatal("removed group with an unresolved orphan marker")
	}
	next, _ := newReviewCapacity(root, 1)
	defer next.Close()
	if slot, err := next.tryAcquire(); err != nil || slot != nil {
		if slot != nil {
			slot.Close()
		}
		t.Fatal("orphan cleanup marker lost restart debt", err)
	}
}
