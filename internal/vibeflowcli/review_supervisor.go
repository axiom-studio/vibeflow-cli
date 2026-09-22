package vibeflowcli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type reviewRunnerStatus struct {
	BindingID string
	Binding   reviewBinding
	State     string
	Message   string
}

type reviewSupervisor struct {
	ctx          context.Context
	cancel       context.CancelFunc
	cfg          *Config // Private immutable snapshot, captured before main TUI commands start.
	configPath   string
	capacity     *reviewCapacity
	mu           sync.Mutex
	owned        map[string]*reviewOwnedRunner
	statuses     []reviewRunnerStatus
	closed       bool
	closeErr     error
	options      reviewWatchOptions
	initialPaths []string
	preferences  map[string]string
}

func newReviewSupervisor(ctx context.Context, cfg *Config, configPath string) (*reviewSupervisor, error) {
	capacity, err := newReviewCapacity(RootDir(), cfg.ReviewConcurrency)
	if err != nil {
		return nil, err
	}
	// Retain only fields consumed by discovery and owned startup. No live model
	// configuration, slice backing store, or environment map crosses this boundary.
	private := &Config{ServerURL: cfg.ServerURL, APIToken: cfg.APIToken, DefaultWorkDir: cfg.DefaultWorkDir, DefaultProvider: cfg.DefaultProvider, LLMGatewayEnabled: cfg.LLMGatewayEnabled, ReviewConcurrency: cfg.ReviewConcurrency, DirectoryHistory: append([]string(nil), cfg.DirectoryHistory...), Providers: map[string]Provider{}, SavedEnvVars: map[string]string{}}
	for key, value := range cfg.SavedEnvVars {
		private.SavedEnvVars[key] = value
	}
	for key, value := range cfg.Providers {
		provider := Provider{Binary: value.Binary, Env: map[string]string{}}
		for k, v := range value.Env {
			provider.Env[k] = v
		}
		private.Providers[key] = provider
	}
	ctx, cancel := context.WithCancel(ctx)
	cwd, _ := os.Getwd()
	sessions, _ := NewStore().readFile()
	s := &reviewSupervisor{ctx: ctx, cancel: cancel, cfg: private, configPath: configPath, capacity: capacity, owned: map[string]*reviewOwnedRunner{}, initialPaths: knownReviewCheckoutPaths(private, cwd, sessions)}
	s.options, s.preferences = loadReviewGroupPreferences(cfg, configPath, cwd)
	return s, nil
}

func cloneReviewStatuses(statuses []reviewRunnerStatus) []reviewRunnerStatus {
	result := append([]reviewRunnerStatus(nil), statuses...)
	for i := range result {
		result[i].Binding.Checkouts = append([]reviewStartupCheckout(nil), result[i].Binding.Checkouts...)
		for j := range result[i].Binding.Checkouts {
			result[i].Binding.Checkouts[j].Links = append([]reviewStartupChoice(nil), result[i].Binding.Checkouts[j].Links...)
		}
	}
	return result
}

func (s *reviewSupervisor) Snapshot() []reviewRunnerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *reviewSupervisor) snapshotLocked() []reviewRunnerStatus {
	result := cloneReviewStatuses(s.statuses)
	for i := range result {
		if runner := s.owned[result[i].BindingID]; runner != nil {
			result[i].Message = runner.Status()
			select {
			case <-runner.Done():
				result[i].State = "failed"
				result[i].Message = "Review runner stopped; press r to retry."
				if err := runner.Err(); err != nil {
					result[i].Message = err.Error()
				}
			default:
				result[i].State = "online"
			}
		}
	}
	return result
}

func closeReviewRunners(runners []*reviewOwnedRunner) error {
	// Signal every owned child before any wait, so one slow provider cannot
	// leave other repositories accepting work throughout its shutdown.
	for _, runner := range runners {
		runner.once.Do(func() { _ = runner.input.Close() })
	}
	errs := make(chan error, len(runners))
	for _, runner := range runners {
		go func() { errs <- runner.Close() }()
	}
	var result error
	for range runners {
		result = errors.Join(result, <-errs)
	}
	return result
}

func (s *reviewSupervisor) Reconcile(d reviewDiscovery) []reviewRunnerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return s.snapshotLocked()
	}
	wanted := map[string]reviewBinding{}
	projects := map[int64]bool{}
	for _, p := range d.Projects {
		projects[p.ID] = true
	}
	for _, b := range d.Bindings {
		wanted[reviewBackgroundID(s.cfg.ServerURL, b.Options)] = b
	}
	var stop []*reviewOwnedRunner
	var preserved []reviewRunnerStatus
	for _, status := range s.statuses {
		id, project := status.BindingID, status.Binding.Options.ProjectID
		_, present := wanted[id]
		transient := !d.Revoked[project] && (d.Problems[project] != "" || (!d.Complete && !projects[project]))
		if !present && transient {
			preserved = append(preserved, status)
			continue
		}
		if !present {
			if runner := s.owned[id]; runner != nil {
				stop = append(stop, runner)
				delete(s.owned, id)
			}
		}
	}
	_ = closeReviewRunners(stop)
	bindings := append([]reviewBinding(nil), d.Bindings...)
	pending := map[string]bool{}
	for id := range wanted {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(s.capacity.Directory), "review-runners", id, "state.json"))
		var state reviewRunnerState
		pending[id] = err == nil && json.Unmarshal(data, &state) == nil && state.Pending != nil
	}
	sort.SliceStable(bindings, func(i, j int) bool {
		return pending[reviewBackgroundID(s.cfg.ServerURL, bindings[i].Options)] && !pending[reviewBackgroundID(s.cfg.ServerURL, bindings[j].Options)]
	})
	statuses := map[string]reviewRunnerStatus{}
	for _, b := range bindings {
		b.Options.Provider, b.Options.Model = s.options.Provider, s.options.Model
		id := reviewBackgroundID(s.cfg.ServerURL, b.Options)
		status := reviewRunnerStatus{BindingID: id, Binding: b, State: "needs_checkout", Message: b.Problem}
		if runner := s.owned[id]; runner != nil {
			select {
			case <-runner.Done():
				delete(s.owned, id)
			default:
				status.State = "online"
				statuses[id] = status
				continue
			}
		}
		if b.Options.Repository != "" && s.ctx.Err() == nil {
			runner, err := startReviewOwnedWithCapacity(s.ctx, s.cfg, s.configPath, b.Options, s.capacity)
			if err == nil {
				s.owned[id] = runner
				status.State = "online"
			} else if errors.Is(err, errReviewOwnedBusy) {
				status.State = "external"
				status.Message = "Already running outside this TUI; left untouched."
			} else {
				status.State = "failed"
				status.Message = err.Error()
			}
		}
		statuses[id] = status
	}
	s.statuses = preserved
	for _, b := range d.Bindings {
		s.statuses = append(s.statuses, statuses[reviewBackgroundID(s.cfg.ServerURL, b.Options)])
	}
	s.statuses = cloneReviewStatuses(s.statuses)
	return s.snapshotLocked()
}

func (s *reviewSupervisor) Close() error {
	s.cancel() // Startup may hold mu while awaiting a child's readiness.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	var runners []*reviewOwnedRunner
	for _, runner := range s.owned {
		runners = append(runners, runner)
	}
	s.closeErr = errors.Join(closeReviewRunners(runners), s.capacity.Close())
	return s.closeErr
}
