package vibeflowcli

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var errReviewLockBusy = errors.New("review runner or child is already active")
var errReviewCleanupUnverified = errors.New("Provider cleanup unverified")

type reviewCapacity struct {
	Directory string `json:"directory"`
	Limit     int    `json:"limit"`
	owner     *os.File
}

type reviewReservation struct {
	Directory string `json:"directory"`
	Slot      string `json:"slot"`
}

type reviewProviderCleanup struct {
	Reservation reviewReservation `json:"reservation"`
	RequestID   string            `json:"request_id"`
	JobID       string            `json:"job_id"`
	AttemptID   string            `json:"attempt_id"`
}

func (r reviewReservation) validate(root string) error {
	n, err := strconv.Atoi(strings.TrimPrefix(r.Slot, "slot-"))
	if err != nil || n < 0 || r.Slot != "slot-"+strconv.Itoa(n) || filepath.Dir(r.Directory) != root || !strings.HasPrefix(filepath.Base(r.Directory), "review-capacity-") {
		return fmt.Errorf("invalid review capacity reservation")
	}
	info, err := os.Lstat(r.Directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("invalid review capacity directory")
	}
	return nil
}

// Scan only the fixed private runner-state layout, never checkout directories.
// Pending receipts are the durable reservation, including after a runner dies.
func reviewCapacityReceipts(root string) ([]reviewReceipt, error) {
	dir := filepath.Join(root, "review-runners")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipts []reviewReceipt
	for _, entry := range entries {
		decoded, err := hex.DecodeString(entry.Name())
		if err != nil || len(decoded) != 16 {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("invalid review runner state directory")
		}
		path := filepath.Join(dir, entry.Name(), "state.json")
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && (!info.Mode().IsRegular() || info.Size() > 2<<20) {
			return nil, fmt.Errorf("invalid review runner state")
		}
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		var state reviewRunnerState
		if err == nil && json.Unmarshal(data, &state) != nil {
			return nil, fmt.Errorf("invalid review runner receipt: %s", path)
		}
		if state.Pending != nil && state.Pending.Capacity != nil {
			if err := state.Pending.Capacity.validate(root); err != nil {
				return nil, err
			}
			receipts = append(receipts, *state.Pending)
		}
		work := filepath.Join(dir, entry.Name(), "work")
		info, err = os.Lstat(work)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("invalid private review work directory: %s", work)
		}
		attempts, err := os.ReadDir(work)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, attempt := range attempts {
			if !attempt.IsDir() || len(attempt.Name()) != 36 || strings.ContainsAny(attempt.Name(), "/\\.\r\n\t") {
				continue
			}
			marker := filepath.Join(work, attempt.Name(), "provider-cleanup-pending.json")
			info, err := os.Lstat(marker)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Size() > 64<<10 {
				return nil, fmt.Errorf("invalid provider cleanup marker: %s", marker)
			}
			data, err := os.ReadFile(marker)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			var cleanup reviewProviderCleanup
			if json.Unmarshal(data, &cleanup) != nil || cleanup.RequestID != attempt.Name() || cleanup.JobID == "" || cleanup.AttemptID == "" {
				return nil, fmt.Errorf("invalid provider cleanup marker: %s", marker)
			}
			if err := cleanup.Reservation.validate(root); err != nil {
				return nil, fmt.Errorf("invalid provider cleanup marker %s: %w", marker, err)
			}
			if state.Pending != nil && state.Pending.RequestID == cleanup.RequestID && state.Pending.Capacity != nil && *state.Pending.Capacity != cleanup.Reservation {
				return nil, fmt.Errorf("provider cleanup reservation changed: %s", marker)
			}
			receipts = append(receipts, reviewReceipt{RequestID: cleanup.RequestID, JobID: cleanup.JobID, Capacity: &cleanup.Reservation})
		}
	}
	return receipts, nil
}

func newReviewCapacity(root string, limit int) (*reviewCapacity, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("review_concurrency must be a positive integer")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "review-capacity-")
	if err != nil {
		return nil, err
	}
	owner, err := lockReviewFile(dir + ".lock")
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	return &reviewCapacity{Directory: dir, Limit: limit, owner: owner}, nil
}

func (c *reviewCapacity) validate() error {
	if c.Limit <= 0 || !filepath.IsAbs(c.Directory) || filepath.Clean(c.Directory) != c.Directory || !strings.HasPrefix(filepath.Base(c.Directory), "review-capacity-") {
		return fmt.Errorf("invalid review capacity group")
	}
	info, err := os.Lstat(c.Directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("review capacity group must be a private directory")
	}
	return nil
}

func (c *reviewCapacity) tryAcquire() (*os.File, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	control, err := lockReviewFile(filepath.Join(filepath.Dir(c.Directory), ".review-capacity.lock"))
	if errors.Is(err, errReviewLockBusy) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer control.Close()
	return c.acquire("")
}

// The root admission lock covers scanning reservations and saving a transfer.
func (c *reviewCapacity) acquire(requestID string) (*os.File, error) {
	receipts, err := reviewCapacityReceipts(filepath.Dir(c.Directory))
	if err != nil {
		return nil, err
	}
	reserved := map[string]bool{}
	seen := map[string]bool{}
	debt := 0
	recovering := false
	for _, receipt := range receipts {
		if receipt.RequestID == requestID {
			recovering = recovering || len(receipt.Result) > 0 || receipt.Failure != "" || receipt.Completed
			continue
		}
		if seen[receipt.RequestID] {
			continue
		}
		seen[receipt.RequestID] = true
		reservation := receipt.Capacity
		if reservation.Directory == c.Directory {
			reserved[reservation.Slot] = true
			continue
		}
		owner, err := lockReviewFile(reservation.Directory + ".lock")
		if errors.Is(err, errReviewLockBusy) {
			continue
		} // Another live TUI is independent.
		if err != nil {
			return nil, err
		}
		owner.Close()
		debt++
	}
	// Count existing locks as well as durable receipts, without allocating up
	// to Limit files. Debt can grow after another TUI exits while we are busy.
	entries, err := os.ReadDir(c.Directory)
	if err != nil {
		return nil, err
	}
	used := len(reserved)
	for _, entry := range entries {
		n, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "slot-"))
		if err != nil || n < 0 || n >= c.Limit || entry.Name() != "slot-"+strconv.Itoa(n) || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("invalid review capacity slot")
		}
		if reserved[entry.Name()] {
			continue
		}
		file, err := lockReviewFile(filepath.Join(c.Directory, entry.Name()))
		if errors.Is(err, errReviewLockBusy) {
			used++
			continue
		}
		if err != nil {
			return nil, err
		}
		file.Close()
	}
	// Transferring an existing receipt does not add debt. Let saved results
	// drain even when the user lowered the limit below the old pending count.
	if used >= c.Limit || (!recovering && used+debt >= c.Limit) {
		return nil, nil
	}
	for i := 0; i < c.Limit; i++ {
		if reserved["slot-"+strconv.Itoa(i)] {
			continue
		}
		file, err := lockReviewFile(filepath.Join(c.Directory, "slot-"+strconv.Itoa(i)))
		if errors.Is(err, errReviewLockBusy) {
			continue
		}
		return file, err
	}
	return nil, nil
}

func (c *reviewCapacity) Close() error {
	if c.owner != nil {
		defer c.owner.Close()
	}
	if err := c.validate(); err != nil {
		return err
	}
	control, err := lockReviewFile(filepath.Join(filepath.Dir(c.Directory), ".review-capacity.lock"))
	if err != nil {
		return err
	}
	defer control.Close()
	receipts, err := reviewCapacityReceipts(filepath.Dir(c.Directory))
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if receipt.Capacity.Directory == c.Directory {
			return fmt.Errorf("review capacity retains a pending receipt")
		}
	}
	entries, err := os.ReadDir(c.Directory)
	if err != nil {
		return err
	}
	var locked []*os.File
	defer func() {
		for _, file := range locked {
			file.Close()
		}
	}()
	for _, entry := range entries {
		n, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "slot-"))
		if err != nil || entry.Name() != "slot-"+strconv.Itoa(n) || n < 0 || n >= c.Limit || !entry.Type().IsRegular() {
			return fmt.Errorf("invalid review capacity slot")
		}
		file, err := lockReviewFile(filepath.Join(c.Directory, entry.Name()))
		if err != nil {
			return err
		}
		locked = append(locked, file)
	}
	for _, file := range locked {
		if err := os.Remove(file.Name()); err != nil {
			return err
		}
	}
	if err := os.Remove(c.Directory); err != nil {
		return err
	}
	return os.Remove(c.Directory + ".lock")
}

func (w *reviewWatch) acquireCapacity() (bool, error) {
	if w.capacity == nil {
		return true, nil
	}
	if w.slot != nil {
		return true, nil
	}
	if err := w.capacity.validate(); err != nil {
		return false, err
	}
	control, err := lockReviewFile(filepath.Join(filepath.Dir(w.capacity.Directory), ".review-capacity.lock"))
	if errors.Is(err, errReviewLockBusy) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer control.Close()
	p := w.state.Pending
	if p == nil {
		return false, fmt.Errorf("review admission requires a receipt identity")
	}
	if err := w.providerCleanupPending(p); err != nil {
		return false, err
	}
	// A surviving guard's duplicate still owns the old slot even if its runner
	// vanished. Never transfer that reservation until the guard closes it.
	var old *os.File
	if p.Capacity != nil {
		if err := p.Capacity.validate(filepath.Dir(w.capacity.Directory)); err != nil {
			return false, err
		}
		old, err = w.lockReviewCleanup(filepath.Join(p.Capacity.Directory, p.Capacity.Slot), p)
		if errors.Is(err, errReviewLockBusy) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		defer func() {
			if old != nil {
				old.Close()
			}
		}()
	}
	var slot *os.File
	if old != nil && p.Capacity.Directory == w.capacity.Directory {
		slot, old = old, nil
	} else {
		slot, err = w.capacity.acquire(p.RequestID)
	}
	if err != nil || slot == nil {
		return false, err
	}
	previous := p.Capacity
	p.Capacity = &reviewReservation{Directory: w.capacity.Directory, Slot: filepath.Base(slot.Name())}
	if err := w.save(); err != nil {
		p.Capacity = previous
		slot.Close()
		return false, err
	}
	w.slot = slot
	return true, nil
}

func (w *reviewWatch) releaseCapacity() {
	if w.slot != nil {
		w.slot.Close()
		w.slot = nil
	}
}

func (w *reviewWatch) lockReviewCleanup(path string, p *reviewReceipt) (*os.File, error) {
	lock, err := lockReviewFile(path)
	if err != nil {
		return nil, err
	}
	// The guard can publish cleanup debt between an earlier check and its
	// death. Recheck only after acquiring the ownership it held while writing.
	if err := w.providerCleanupPending(p); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

func (w *reviewWatch) providerCleanupPending(p *reviewReceipt) error {
	if p != nil && (len(p.RequestID) != 36 || strings.ContainsAny(p.RequestID, "/\\.\r\n\t")) {
		return fmt.Errorf("unsafe review receipt directory")
	}
	work := filepath.Join(w.root, "work")
	attempts, err := os.ReadDir(work)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		if !attempt.IsDir() || len(attempt.Name()) != 36 || strings.ContainsAny(attempt.Name(), "/\\.\r\n\t") {
			continue
		}
		path := filepath.Join(work, attempt.Name(), "provider-cleanup-pending.json")
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", errReviewCleanupUnverified, path)
	}
	return nil
}
