package plan

import "errors"

// Item is a tracked work item.
type Item struct {
	ID     int64
	Status string
	Plan   string // "starter" or "team"
}

// ErrUnknownStatus means the status is not one of Statuses.
var ErrUnknownStatus = errors.New("unknown status")

// Statuses in workflow order.
var Statuses = []string{"open", "planning", "implementing", "in_review", "done"}

// UpdateStatus moves item to status to. Agents re-send their current status
// when they resume a session, so an update to the current status is a
// successful no-op.
func UpdateStatus(item *Item, to string) error {
	if rank(to) < 0 {
		return ErrUnknownStatus
	}
	if item.Plan == "starter" {
		if err := starterGate(item.Status, to); err != nil {
			return err
		}
	}
	if item.Status == to {
		return nil
	}
	item.Status = to
	return nil
}

func rank(status string) int {
	for i, s := range Statuses {
		if s == status {
			return i
		}
	}
	return -1
}
