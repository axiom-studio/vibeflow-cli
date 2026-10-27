package plan

import "errors"

// ErrStarterBackward means a Starter plan item tried to move backward.
var ErrStarterBackward = errors.New("starter plan items only move forward")

// starterGate keeps Starter plan items moving forward through the workflow;
// reopening finished work is a Team plan feature.
func starterGate(from, to string) error {
	if rank(to) <= rank(from) {
		return ErrStarterBackward
	}
	return nil
}
