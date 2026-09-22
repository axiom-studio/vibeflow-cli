package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type reviewWatchOptions struct {
	Project          string
	ProjectID        int64
	Repository       string
	RepositoryLinkID int64
	GitProvider      string
	Provider         string
	Model            string
	Kind             string
	Name             string
	Once             bool
	PollInterval     time.Duration
	Timeout          time.Duration
}

type reviewReceipt struct {
	JobID     string             `json:"job_id"`
	RequestID string             `json:"request_id"`
	Execution *reviewExecution   `json:"execution,omitempty"`
	Result    json.RawMessage    `json:"result,omitempty"`
	Failure   string             `json:"failure,omitempty"`
	Completed bool               `json:"completed"`
	Capacity  *reviewReservation `json:"capacity,omitempty"`
}

type reviewRunnerState struct {
	ID      string         `json:"id"`
	OwnerID int64          `json:"owner_id"`
	Cursor  string         `json:"cursor,omitempty"`
	Pending *reviewReceipt `json:"pending,omitempty"`
}

type reviewWatch struct {
	client        *Client
	cfg           *Config
	options       reviewWatchOptions
	root          string
	state         reviewRunnerState
	output        io.Writer
	providerReady bool
	onReady       func()
	onStatus      func(string)
	capacity      *reviewCapacity
	slot          *os.File
}

func reviewUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func saveReviewJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".review-write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func reviewWatchCmd() *cobra.Command {
	o := reviewWatchOptions{Kind: "local", PollInterval: 5 * time.Second, Timeout: 15 * time.Minute}
	var background, status, owned bool
	var stop, managed, serverURL string
	cmd := &cobra.Command{Use: "review-watch", Short: "Run fresh, isolated PR reviews while this runner is online", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if owned {
			return runReviewOwned(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		}
		if managed != "" {
			return runReviewBackground(cmd.Context(), managed)
		}
		if status {
			return reviewBackgroundStatusList(cmd.OutOrStdout())
		}
		if stop != "" {
			return stopReviewBackground(cmd.Context(), stop, cmd.OutOrStdout())
		}
		if background && (!cmd.Flags().Changed("repo") || !cmd.Flags().Changed("project") || !cmd.Flags().Changed("repository-link")) {
			return fmt.Errorf("background runners require explicit --repo, --project, and --repository-link")
		}
		path := flagConfigPath
		if path == "" {
			path = ConfigPath()
		}
		load := LoadConfig
		if background {
			load = loadReviewBackgroundConfig
		}
		cfg, err := load(path)
		if err != nil {
			return err
		}
		if background {
			if token := os.Getenv("VIBEFLOW_TOKEN"); token != "" && token != cfg.APIToken {
				return fmt.Errorf("background runner credentials must come from the selected config file")
			}
			if origin := os.Getenv("VIBEFLOW_URL"); origin != "" {
				cfg.ServerURL = origin
			}
		}
		if serverURL != "" {
			cfg.ServerURL = serverURL
		}
		if o.Provider == "" {
			o.Provider = cfg.DefaultProvider
		}
		if o.Project == "" {
			o.Project = cfg.DefaultProject
		}
		if o.Repository == "" {
			o.Repository, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		o.Repository, err = filepath.Abs(o.Repository)
		if err != nil {
			return err
		}
		if o.Name == "" {
			o.Name, _ = os.Hostname()
		}
		if o.RepositoryLinkID <= 0 || o.Project == "" || (o.Kind != "local" && o.Kind != "shared") || o.Timeout < time.Minute || o.Timeout > time.Hour {
			return fmt.Errorf("provide --project and --repository-link; timeout must be 1m to 1h")
		}
		// Heartbeats ride on each idle poll, so a long interval makes the server
		// mark this runner offline and route its work to shared runners.
		if o.PollInterval < time.Second || o.PollInterval > time.Minute {
			return fmt.Errorf("interval must be between 1s and 60s; the server treats a runner as offline after 2 minutes")
		}
		if o.GitProvider != "github" && o.GitProvider != "bitbucket" {
			return fmt.Errorf("review repository integration must be github or bitbucket")
		}
		if strings.TrimSpace(o.Name) == "" || strings.ContainsAny(o.Name+o.Model, "\x00\r\n\t") || len(o.Name) > 100 || len(o.Model) > 200 {
			return fmt.Errorf("invalid runner name or model; runner names must be 1 to 100 bytes")
		}
		if o.Provider != "claude" && o.Provider != "codex" {
			return fmt.Errorf("review-watch supports --provider claude or codex")
		}
		if cfg.APIToken == "" {
			return fmt.Errorf("connect VibeFlow before starting a review runner")
		}
		client := NewClient(cfg.ServerURL, cfg.APIToken)
		o.ProjectID, err = strconv.ParseInt(o.Project, 10, 64)
		if err != nil {
			var projects []Project
			e := client.reviewRequest(cmd.Context(), "GET", "/projects", nil, &projects)
			if e != nil {
				return fmt.Errorf("could not load review projects")
			}
			o.ProjectID = 0
			for _, p := range projects {
				if p.Name == o.Project {
					if o.ProjectID != 0 {
						return fmt.Errorf("project name is ambiguous; use its numeric ID")
					}
					o.ProjectID = p.ID
				}
			}
		}
		if o.ProjectID <= 0 {
			return fmt.Errorf("review project not found")
		}
		if background {
			return startReviewBackground(cmd.Context(), cfg, path, o, cmd.OutOrStdout())
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer cancel()
		watch := &reviewWatch{client: client, cfg: cfg, options: o, output: cmd.OutOrStdout()}
		return watch.run(ctx)
	}}
	cmd.Flags().StringVar(&o.Project, "project", "", "VibeFlow project name or ID")
	cmd.Flags().StringVar(&o.Repository, "repo", "", "Local checkout for the linked repository (default: current directory)")
	cmd.Flags().Int64Var(&o.RepositoryLinkID, "repository-link", 0, "VibeFlow repository link ID")
	cmd.Flags().StringVar(&o.GitProvider, "git-provider", "github", "Repository integration: github or bitbucket")
	cmd.Flags().StringVar(&o.Provider, "provider", "", "Model provider: claude or codex (default: configured provider); codex needs an OpenAI API key, not a ChatGPT login")
	cmd.Flags().StringVar(&o.Model, "model", "", "Model selection; required for gateway reviews")
	cmd.Flags().StringVar(&o.Kind, "runner-kind", "local", "local or explicitly authorized shared runner")
	cmd.Flags().StringVar(&o.Name, "name", "", "Runner name (default: hostname)")
	cmd.Flags().DurationVar(&o.PollInterval, "interval", o.PollInterval, "Idle API polling interval, 1s to 60s; each poll also heartbeats the runner, no LLM runs while idle")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", o.Timeout, "Maximum time per attempt, also bounded by the server deadline")
	cmd.Flags().BoolVar(&o.Once, "once", false, "Check one page of available work and exit after at most one review")
	cmd.Flags().BoolVar(&background, "background", false, "Run this explicit binding in the background independently of the TUI")
	cmd.Flags().BoolVar(&status, "status", false, "Show managed review runners in this root")
	cmd.Flags().StringVar(&stop, "stop", "", "Stop and disable a detached runner ID")
	cmd.Flags().StringVar(&managed, "managed-runner", "", "Internal managed runner binding ID")
	cmd.Flags().BoolVar(&owned, "owned-runner", false, "Internal runner owned by this CLI's liveness pipe")
	cmd.Flags().StringVar(&serverURL, "server-url", "", "VibeFlow server URL (pinned for background runners)")
	_ = cmd.Flags().MarkHidden("managed-runner")
	_ = cmd.Flags().MarkHidden("owned-runner")
	cmd.MarkFlagsMutuallyExclusive("background", "status", "stop", "managed-runner", "owned-runner")
	return cmd
}

func (w *reviewWatch) prefix() string {
	return fmt.Sprintf("/projects/%d/pr-review-runners/%s", w.options.ProjectID, w.state.ID)
}
func (w *reviewWatch) attemptPath(p *reviewReceipt) string {
	return w.prefix() + "/jobs/" + url.PathEscape(p.JobID) + "/attempts/" + url.PathEscape(p.Execution.Attempt.ID)
}
func (w *reviewWatch) save() error {
	return saveReviewJSON(filepath.Join(w.root, "state.json"), &w.state)
}
func (w *reviewWatch) workDir(p *reviewReceipt) string {
	return filepath.Join(w.root, "work", p.RequestID)
}

func (w *reviewWatch) run(ctx context.Context) error {
	defer w.releaseCapacity()
	identity := fmt.Sprintf("%s\n%d\n%d\n%s\n%s\n%s", w.client.baseURL, w.options.ProjectID, w.options.RepositoryLinkID, w.options.GitProvider, w.options.Kind, w.options.Name)
	digest := sha256.Sum256([]byte(identity))
	// The guard and provider both change cwd. Their private paths must keep
	// referring to the supervisor's root even when the user passed --root .
	var err error
	w.root, err = filepath.Abs(filepath.Join(RootDir(), "review-runners", hex.EncodeToString(digest[:16])))
	if err != nil {
		return fmt.Errorf("could not resolve the review runner directory")
	}
	if err := os.MkdirAll(filepath.Join(w.root, "work"), 0700); err != nil {
		return err
	}
	lock, err := lockReviewFile(filepath.Join(w.root, "runner.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := os.ReadFile(filepath.Join(w.root, "state.json"))
	if err == nil {
		if len(data) > 2<<20 || json.Unmarshal(data, &w.state) != nil {
			return fmt.Errorf("review runner receipt is invalid; preserve it for recovery")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if w.state.ID == "" {
		w.state.ID = reviewUUID()
		if err = w.save(); err != nil {
			return err
		}
	}
	var registered struct {
		ID               string `json:"id"`
		UserID           int64  `json:"user_id"`
		Provider         string `json:"provider"`
		RepositoryLinkID int64  `json:"repository_link_id"`
	}
	if err = w.client.reviewRequest(ctx, "POST", fmt.Sprintf("/projects/%d/pr-review-runners", w.options.ProjectID), map[string]any{"id": w.state.ID, "kind": w.options.Kind, "name": w.options.Name, "provider": w.options.GitProvider, "repository_link_id": w.options.RepositoryLinkID}, &registered); err != nil {
		return err
	}
	if registered.ID != w.state.ID || registered.UserID <= 0 || (w.state.OwnerID != 0 && w.state.OwnerID != registered.UserID) {
		return fmt.Errorf("review runner owner changed; use a separate runner name")
	}
	if registered.Provider != w.options.GitProvider || registered.RepositoryLinkID != w.options.RepositoryLinkID {
		return fmt.Errorf("review runner repository scope was not confirmed; upgrade the server and re-register this runner")
	}
	w.state.OwnerID = registered.UserID
	if err = w.save(); err != nil {
		return err
	}
	defer func() {
		if w.state.Pending == nil {
			stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			w.client.reviewRequest(stopCtx, "DELETE", w.prefix(), nil, nil)
		}
	}()
	fmt.Fprintf(w.output, "Review runner %s is online (%s, %s). Ctrl-C stops it.\n", w.options.Name, w.options.Kind, w.options.Provider)
	// An interrupted attempt always fails or replays its saved result. It never
	// resumes the old model conversation or launches a second child for it.
	if w.state.Pending != nil {
		if w.state.Pending.Execution != nil && len(w.state.Pending.Result) == 0 && w.state.Pending.Failure == "" {
			w.state.Pending.Failure = "Review supervisor stopped before saving a result"
			if err = w.save(); err != nil {
				return err
			}
		}
	}
	backoff := w.options.PollInterval
	status := ""
	for {
		if ctx.Err() != nil {
			return nil
		}
		err = w.poll(ctx)
		if ctx.Err() != nil {
			return nil
		}
		nextStatus := status
		if err == nil {
			nextStatus = ""
		}
		if errors.Is(err, errReviewCleanupUnverified) {
			nextStatus = err.Error() // Locally constructed private marker path only.
		} else {
			var response *reviewHTTPError
			if w.state.Pending != nil && errors.As(err, &response) && (response.Status == 401 || response.Status == 403) {
				nextStatus = fmt.Sprintf("Review result pending authorization (HTTP %d)", response.Status)
			}
		}
		// A heartbeat failure must not hide unresolved provider cleanup.
		cleanupErr := w.providerCleanupPending(w.state.Pending)
		quarantined := errors.Is(cleanupErr, errReviewCleanupUnverified)
		if quarantined {
			nextStatus = cleanupErr.Error()
		}
		if status != nextStatus {
			status = nextStatus
			if w.onStatus != nil {
				w.onStatus(status)
			}
			if status != "" {
				fmt.Fprintln(w.output, status)
			}
		}
		if err != nil {
			var response *reviewHTTPError
			retry := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errReviewConnection) || errors.Is(err, errReviewCleanupUnverified) || (errors.As(err, &response) && (response.Status >= 500 || response.Status == 429 || ((w.state.Pending != nil || quarantined) && (response.Status == 401 || response.Status == 403))))
			if w.options.Once || !retry {
				return err
			}
			fmt.Fprintf(w.output, "Review API unavailable; retrying in %s. The pending receipt is safe.\n", backoff)
		} else {
			backoff = w.options.PollInterval
			if w.options.Once {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if err != nil && backoff < time.Minute {
			backoff *= 2
			if backoff > time.Minute {
				backoff = time.Minute
			}
		}
	}
}

func (w *reviewWatch) poll(ctx context.Context) error {
	// Recovery needs only server authorization, even if the provider was removed
	// or logged out after a completed result was saved.
	pending := w.state.Pending
	cleanupErr := w.providerCleanupPending(pending)
	needsProvider := cleanupErr == nil && (pending == nil || (pending.Execution == nil && len(pending.Result) == 0 && pending.Failure == ""))
	if needsProvider && !w.providerReady {
		if err := preflightReviewProvider(ctx, w.cfg, w.options.Provider, w.options.Model); err != nil {
			return err
		}
		w.providerReady = true
	}

	if err := w.client.reviewRequest(ctx, "POST", w.prefix()+"/heartbeat", struct{}{}, nil); err != nil {
		return err
	}
	admitted := true
	admissionErr := cleanupErr
	if admissionErr == nil && w.state.Pending != nil {
		admitted, admissionErr = w.acquireCapacity()
	}
	if w.onReady != nil {
		w.onReady()
		w.onReady = nil
	}
	if admissionErr != nil {
		return admissionErr
	}
	if !admitted {
		return nil
	}
	if w.state.Pending == nil {
		var page struct {
			Reviews []reviewJob `json:"reviews"`
			Next    string      `json:"next_after_id"`
		}
		path := w.prefix() + "/work?limit=100&after_id=" + url.QueryEscape(w.state.Cursor)
		if err := w.client.reviewRequest(ctx, "GET", path, nil, &page); err != nil {
			return err
		}
		if page.Next != "" && page.Next <= w.state.Cursor {
			return fmt.Errorf("review work cursor did not advance")
		}
		for _, job := range page.Reviews {
			if job.RepositoryLinkID == w.options.RepositoryLinkID && job.Provider == w.options.GitProvider {
				w.state.Pending = &reviewReceipt{JobID: job.ID, RequestID: reviewUUID()}
				acquired, err := w.acquireCapacity()
				if err != nil || !acquired {
					w.state.Pending = nil
					return err
				}
				break
			}
		}
		w.state.Cursor = page.Next
		if err := w.save(); err != nil {
			return err
		}
	}
	return w.advance(ctx, true)
}

func (w *reviewWatch) advance(ctx context.Context, fresh bool) error {
	p := w.state.Pending
	if p == nil {
		return nil
	}
	if p.Completed {
		return w.finishReceipt(p)
	}
	if p.Execution == nil {
		var execution reviewExecution
		err := w.client.reviewRequest(ctx, "POST", w.prefix()+"/jobs/"+url.PathEscape(p.JobID)+"/claim", map[string]string{"request_id": p.RequestID}, &execution)
		if err != nil {
			if reviewPermanent(err) {
				p.Completed = true
				return w.finishReceipt(p)
			}
			return err
		}
		if execution.Attempt.ID == "" {
			p.Completed = true
			return w.finishReceipt(p)
		}
		if execution.Version != 2 || execution.Review.ID != p.JobID || execution.Attempt.Round.JobID != p.JobID || execution.Attempt.RunnerID != w.state.ID || execution.Prompt == "" || execution.Attempt.Round.HeadSHA != execution.Review.HeadSHA || execution.Attempt.Round.BaseSHA != execution.Review.BaseSHA || execution.Review.RepositoryLinkID != w.options.RepositoryLinkID || execution.Review.Provider != w.options.GitProvider {
			return fmt.Errorf("review execution identity or version is invalid")
		}
		p.Execution = &execution
		if err = w.save(); err != nil {
			return err
		}
		// A recovered lost claim response has not started a child, so running it
		// is safe. A persisted claimed attempt takes the failure branch above.
		fresh = true
	}
	if len(p.Result) == 0 && p.Failure == "" && fresh {
		fmt.Fprintf(w.output, "Reviewing %s at %.12s with a fresh Principal Engineer.\n", p.JobID, p.Execution.Attempt.Round.HeadSHA)
		result, err := w.execute(ctx, p)
		if err != nil {
			p.Failure = err.Error()
		} else {
			p.Result = result
		}
		if err = w.save(); err != nil {
			return err
		}
	}
	// Cleanup precedes network publication, and the exact submission remains
	// durable even if a successful server response is lost.
	if err := w.cleanup(p); err != nil {
		return err
	}
	submitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var err error
	if len(p.Result) > 0 && p.Failure == "" {
		err = w.client.reviewRequest(submitCtx, "POST", w.attemptPath(p)+"/result", p.Result, nil)
		var response *reviewHTTPError
		if errors.As(err, &response) && response.Status == 400 {
			p.Failure = "Structured review result was rejected by the server"
			if saveErr := w.save(); saveErr != nil {
				return saveErr
			}
			err = w.client.reviewRequest(submitCtx, "POST", w.attemptPath(p)+"/fail", map[string]string{"reason": p.Failure}, nil)
		}
	} else {
		err = w.client.reviewRequest(submitCtx, "POST", w.attemptPath(p)+"/fail", map[string]string{"reason": p.Failure}, nil)
	}
	var response *reviewHTTPError
	if err != nil && !(errors.As(err, &response) && (response.Status == 404 || response.Status == 409)) {
		return err
	}
	if err != nil {
		fmt.Fprintln(w.output, "Review attempt is no longer accepted; its receipt was retained.")
	} else if p.Failure != "" {
		fmt.Fprintln(w.output, "Review attempt failed:", p.Failure)
	} else {
		fmt.Fprintln(w.output, "Review result accepted. The server handles tickets and PR publication.")
	}
	p.Completed = true
	return w.finishReceipt(p)
}

func reviewPermanent(err error) bool {
	var e *reviewHTTPError
	return errors.As(err, &e) && (e.Status == 400 || e.Status == 401 || e.Status == 403 || e.Status == 404 || e.Status == 409)
}

func (w *reviewWatch) finishReceipt(p *reviewReceipt) error {
	// Keep one acknowledged receipt; pending submissions are never evicted.
	if err := saveReviewJSON(filepath.Join(w.root, "last-receipt.json"), p); err != nil {
		return err
	}
	w.state.Pending = nil
	if err := w.save(); err != nil {
		w.state.Pending = p
		return err
	}
	w.releaseCapacity()
	return nil
}

func (w *reviewWatch) cleanup(p *reviewReceipt) error {
	if len(p.RequestID) != 36 || strings.ContainsAny(p.RequestID, "/\\.") {
		return fmt.Errorf("unsafe review receipt directory")
	}
	dir := w.workDir(p)
	if err := w.providerCleanupPending(p); err != nil {
		return err
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	// The child guard holds this lock until every provider process has stopped.
	deadline := time.Now().Add(8 * time.Second)
	for {
		lock, err := w.lockReviewCleanup(filepath.Join(dir, "child.lock"), p)
		if err == nil {
			defer lock.Close()
			return os.RemoveAll(dir)
		}
		if !errors.Is(err, errReviewLockBusy) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("prior review child is still shutting down; restart the watcher to retry cleanup")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (w *reviewWatch) execute(parent context.Context, p *reviewReceipt) (_ json.RawMessage, failureErr error) {
	started := time.Now()
	stageStarted := started
	diagnostic := reviewExecutionDiagnostic{Version: 1, AttemptID: p.Execution.Attempt.ID, Provider: w.options.Provider, Stage: "brief"}
	setStage := func(stage string) {
		diagnostic.Stage = stage
		stageStarted = time.Now()
	}
	deadline := time.UnixMilli(p.Execution.Attempt.Round.DeadlineAt)
	if local := time.Now().Add(w.options.Timeout); local.Before(deadline) {
		deadline = local
	}
	deadlineCtx, cancelDeadline := context.WithDeadline(parent, deadline)
	defer cancelDeadline()
	ctx, cancel := context.WithCancelCause(deadlineCtx)
	defer cancel(nil)
	var progress atomic.Uint32
	progressSignal := make(chan struct{}, 1)
	progressAttempted := make(chan struct{}, 1)
	var attemptedProgress atomic.Uint32
	markProgress := func(bit uint32) {
		for {
			old := progress.Load()
			if old&bit != 0 || !progress.CompareAndSwap(old, old|bit) {
				if old&bit != 0 {
					return
				}
				continue
			}
			select {
			case progressSignal <- struct{}{}:
			default:
			}
			return
		}
	}
	// This loop cancels the child at the last acknowledged lease expiry, even
	// when renewal fails because the network is offline.
	renewDone := make(chan struct{})
	defer func() { cancel(nil); <-renewDone }()
	go func() {
		defer close(renewDone)
		expiry := time.UnixMilli(p.Execution.Attempt.LeaseExpiresAt)
		for {
			delay := 10 * time.Second
			if left := time.Until(expiry); left < delay {
				delay = left
			}
			if delay <= 0 {
				cancel(errReviewLeaseExpired)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			case <-progressSignal:
			}
			callCtx, stop := context.WithDeadline(ctx, expiry)
			var renewed reviewExecution
			milestones := progress.Load()
			err := w.client.reviewRequest(callCtx, "POST", w.prefix()+"/heartbeat", struct{}{}, nil)
			renewAttempted := false
			if err == nil {
				renewAttempted = true
				err = w.client.reviewRequest(callCtx, "POST", w.attemptPath(p)+"/renew", reviewRenewBody(*p.Execution, milestones&1 != 0, milestones&2 != 0), &renewed)
			}
			stop()
			if renewAttempted {
				attemptedProgress.Store(milestones)
				select {
				case progressAttempted <- struct{}{}:
				default:
				}
			}
			if err != nil {
				if reviewPermanent(err) {
					cancel(errReviewLeaseRejected)
					return
				}
				if !time.Now().Before(expiry) {
					cancel(errReviewLeaseExpired)
					return
				}
				continue
			}
			if renewed.Attempt.ID != p.Execution.Attempt.ID || renewed.Attempt.Round.ID != p.Execution.Attempt.Round.ID {
				cancel(errReviewLeaseIdentity)
				return
			}
			expiry = time.UnixMilli(renewed.Attempt.LeaseExpiresAt)
		}
	}()
	// Capture the cause before our cleanup defers cancel the execution context.
	// Raw errors and child output are deliberately never persisted or relayed.
	defer func() {
		if failureErr == nil {
			return
		}
		if category := reviewCancellationCategory(ctx); category != "" {
			diagnostic.Category = category
		} else if diagnostic.Category == "" {
			diagnostic.Category = "execution_failed"
			var response *reviewHTTPError
			if errors.As(failureErr, &response) {
				diagnostic.Category = "review_api_error"
				diagnostic.APIErrorStatus = response.Status
			} else if errors.Is(failureErr, errReviewConnection) {
				diagnostic.Category = "review_api_unavailable"
			}
		}
		diagnostic.DurationMS = time.Since(started).Milliseconds()
		diagnostic.StageDurationMS = time.Since(stageStarted).Milliseconds()
		diagnostic.RecordedAt = time.Now().UnixMilli()
		if err := saveReviewJSON(filepath.Join(w.root, "last-provider-diagnostic.json"), diagnostic); err != nil {
			failureErr = fmt.Errorf("review failed at %s (%s); private diagnostic could not be saved", diagnostic.Stage, diagnostic.Category)
			return
		}
		failureErr = diagnostic.failure()
	}()
	root := w.workDir(p)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	var brief reviewBrief
	if err := w.client.reviewRequest(ctx, "GET", w.attemptPath(p)+"/brief", nil, &brief); err != nil {
		return nil, err
	}
	if brief.RoundID != p.Execution.Attempt.Round.ID {
		return nil, fmt.Errorf("review brief belongs to another round")
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(brief.Content, &content) != nil {
		return nil, fmt.Errorf("invalid review brief")
	}
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(brief.Content))
	decoder.UseNumber()
	if err := decoder.Decode(&canonical); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	if brief.Digest != hex.EncodeToString(digest[:]) {
		return nil, fmt.Errorf("review brief digest does not match its content")
	}
	setStage("checkout")
	if err := prepareReviewCheckout(ctx, w.options.Repository, root, p.Execution); err != nil {
		return nil, err
	}
	markProgress(1)
	if err := os.WriteFile(filepath.Join(root, "input", "brief.json"), brief.Content, 0600); err != nil {
		return nil, err
	}
	var findings []json.RawMessage
	setStage("findings")
	json.Unmarshal(content["findings"], &findings)
	var after string
	json.Unmarshal(content["findings_after_id"], &after)
	for pages := 0; after != ""; pages++ {
		if pages >= 20 {
			return nil, fmt.Errorf("prior findings exceed this review's bounded capacity")
		}
		var page struct {
			Findings []json.RawMessage `json:"findings"`
			Next     string            `json:"next_after_id"`
		}
		if err := w.client.reviewRequest(ctx, "GET", w.attemptPath(p)+"/findings?limit=100&after_id="+url.QueryEscape(after), nil, &page); err != nil {
			return nil, err
		}
		if page.Next != "" && page.Next <= after {
			return nil, fmt.Errorf("review findings cursor did not advance")
		}
		findings = append(findings, page.Findings...)
		after = page.Next
	}
	var findingBytes int
	for _, finding := range findings {
		findingBytes += len(finding)
	}
	if findingBytes > 2<<20 {
		return nil, fmt.Errorf("prior finding evidence exceeds the 2 MiB review input limit")
	}
	if err := saveReviewJSON(filepath.Join(root, "input", "prior-findings.json"), findings); err != nil {
		return nil, err
	}
	setStage("provider_setup")
	relayURL, relayToken := "", ""
	if w.cfg.LLMGatewayEnabled {
		var closeRelay func()
		var err error
		relayURL, relayToken, closeRelay, err = startReviewRelay(ctx, w.client, w.options.Provider, w.options.Model)
		if err != nil {
			return nil, err
		}
		defer closeRelay()
	}
	spec, err := prepareReviewProvider(ctx, w.cfg, w.options.Provider, w.options.Model, root, p.Execution, &brief, relayURL, relayToken)
	if err != nil {
		return nil, err
	}
	spec.DeadlineAt = deadline.UnixMilli()
	if w.slot != nil {
		spec.CapacityFD = 3
		spec.Cleanup = &reviewProviderCleanup{Reservation: *p.Capacity, RequestID: p.RequestID, JobID: p.JobID, AttemptID: p.Execution.Attempt.ID}
	}
	if err = saveReviewJSON(filepath.Join(root, "child.json"), spec); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	guard := exec.Command(executable, "review-child", filepath.Join(root, "child.json"))
	if w.slot != nil {
		guard.ExtraFiles = []*os.File{w.slot}
	}
	setStage("child_guard")
	guard.WaitDelay = 250 * time.Millisecond
	guard.Env = []string{"PATH=" + os.Getenv("PATH")}
	guard.Dir = root
	var stdout, stderr limitedReviewBuffer
	stdout.limit = 8 << 20
	stderr.limit = 64 << 10
	guard.Stdout = &stdout
	guard.Stderr = &stderr
	pipe, err := guard.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err = guard.Start(); err != nil {
		pipe.Close()
		return nil, fmt.Errorf("could not start review child guard")
	}
	// EOF on this pipe is an independent parent-death signal. The guard owns
	// termination even when this supervisor is killed without running defers.
	if _, err = pipe.Write([]byte{'R'}); err != nil {
		pipe.Close()
		guard.Wait()
		return nil, fmt.Errorf("review child guard failed")
	}
	done := make(chan error, 1)
	go func() { done <- guard.Wait() }()
	select {
	case err = <-done:
		pipe.Close()
	case <-ctx.Done():
		pipe.Close()
		err = <-done
	}
	diagnostic.StdoutBytes, diagnostic.StderrBytes = stdout.Len(), stderr.Len()
	if report, ok := readReviewProcessReport(filepath.Join(root, "child-diagnostic.json")); ok {
		diagnostic.Stage = "provider"
		stageStarted = time.Now().Add(-time.Duration(report.DurationMS) * time.Millisecond)
		diagnostic.ExitCode, diagnostic.Signal = report.ExitCode, report.Signal
		diagnostic.ProviderDurationMS = report.DurationMS
		if report.Category != "completed" {
			diagnostic.Category = report.Category
		}
	} else if guard.ProcessState != nil {
		code := guard.ProcessState.ExitCode()
		diagnostic.ExitCode = &code
		diagnostic.Signal = reviewProcessSignal(guard.ProcessState)
		diagnostic.Category = "child_guard_failed"
	}
	// Claude returns structured API failures even when its process exits 1.
	// Keep only a numeric HTTP status and our category, never provider text.
	if ctx.Err() == nil && w.options.Provider == "claude" {
		var failure struct {
			Error  bool `json:"is_error"`
			Status int  `json:"api_error_status"`
		}
		if json.Unmarshal(stdout.Bytes(), &failure) == nil && failure.Error {
			category := "provider_error"
			switch failure.Status {
			case 400:
				category = "invalid_request"
			case 401:
				category = "authentication_required"
			case 403:
				category = "access_denied"
			case 429:
				category = "rate_limited"
			default:
				if failure.Status >= 500 && failure.Status <= 599 {
					category = "provider_unavailable"
				}
			}
			if failure.Status < 400 || failure.Status > 599 {
				failure.Status = 0
			}
			diagnostic.Category, diagnostic.APIErrorStatus = category, failure.Status
			return nil, diagnostic.failure()
		}
	}
	if err != nil || ctx.Err() != nil {
		return nil, diagnostic.failure()
	}
	setStage("result")
	diagnostic.Category = "invalid_result"
	var output []byte
	if w.options.Provider == "codex" {
		output, err = os.ReadFile(filepath.Join(root, "provider-result.json"))
	} else {
		var envelope struct {
			Structured json.RawMessage `json:"structured_output"`
		}
		err = json.Unmarshal(stdout.Bytes(), &envelope)
		output = envelope.Structured
	}
	if err != nil || len(output) > 256<<10 {
		return nil, fmt.Errorf("review provider returned no bounded structured result")
	}
	var envelope struct {
		Result  json.RawMessage `json:"result"`
		Failure string          `json:"failure_reason"`
	}
	if json.Unmarshal(output, &envelope) != nil {
		return nil, fmt.Errorf("review provider returned invalid JSON")
	}
	if envelope.Failure != "" || len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		if envelope.Failure != "" {
			diagnostic.Category = "provider_reported_failure"
		}
		return nil, diagnostic.failure()
	}
	var result struct {
		Version int    `json:"schema_version"`
		Head    string `json:"head_sha"`
		Base    string `json:"base_sha"`
		Digest  string `json:"brief_digest"`
	}
	if json.Unmarshal(envelope.Result, &result) != nil || result.Version != 1 || result.Head != p.Execution.Attempt.Round.HeadSHA || result.Base != p.Execution.Attempt.Round.BaseSHA || result.Digest != brief.Digest {
		return nil, fmt.Errorf("review result does not match the claimed revision and brief")
	}
	markProgress(2)
	for attemptedProgress.Load()&2 == 0 {
		select {
		case <-progressAttempted:
		case <-ctx.Done():
			return nil, diagnostic.failure()
		}
	}
	if ctx.Err() != nil {
		return nil, diagnostic.failure()
	}
	return envelope.Result, nil
}

func reviewChildCmd() *cobra.Command {
	return &cobra.Command{Use: "review-child <private-spec>", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		lock, err := lockReviewFile(filepath.Join(filepath.Dir(path), "child.lock"))
		if err != nil {
			return err
		}
		defer lock.Close()
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) > 128<<10 {
			return fmt.Errorf("review child spec too large")
		}
		var spec reviewChildSpec
		if json.Unmarshal(data, &spec) != nil {
			return fmt.Errorf("invalid review child spec")
		}
		capacity, err := retainReviewCapacityFD(spec.CapacityFD, spec.Cleanup)
		if err != nil {
			return err
		}
		if capacity != nil {
			defer capacity.Close()
		}
		signalCtx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stopSignals()
		ctx, cancel := context.WithDeadline(signalCtx, time.UnixMilli(spec.DeadlineAt))
		defer cancel()
		supervisor := cmd.InOrStdin()
		if closer, ok := supervisor.(io.Closer); ok {
			defer closer.Close()
		}
		ready := make(chan error, 1)
		go func() {
			first := make([]byte, 1)
			if _, err := io.ReadFull(supervisor, first); err != nil || first[0] != 'R' {
				ready <- fmt.Errorf("review supervisor is unavailable")
				return
			}
			ready <- nil
			io.Copy(io.Discard, supervisor)
			cancel()
		}()
		select {
		case err := <-ready:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		input, err := os.Open(spec.InputFile)
		if err != nil {
			return err
		}
		defer input.Close()
		child := exec.Command(spec.Binary, spec.Args...)
		child.Dir = spec.Dir
		child.Env = spec.Env
		child.Stdin = input
		child.Stdout = cmd.OutOrStdout()
		child.Stderr = cmd.ErrOrStderr()
		marker := filepath.Join(filepath.Dir(path), "provider-cleanup-pending.json")
		if spec.Cleanup != nil {
			if filepath.Base(filepath.Dir(path)) != spec.Cleanup.RequestID {
				return fmt.Errorf("invalid review cleanup identity")
			}
			if err := saveReviewJSON(marker, spec.Cleanup); err != nil {
				return err
			}
		}
		started := time.Now()
		err = runReviewProcess(ctx, child)
		if spec.Cleanup != nil && !errors.Is(err, errReviewCleanupUnverified) {
			if removeErr := os.Remove(marker); removeErr != nil {
				return fmt.Errorf("could not confirm provider cleanup: %w", removeErr)
			}
			dir, openErr := os.Open(filepath.Dir(path))
			if openErr != nil {
				return openErr
			}
			syncErr := dir.Sync()
			dir.Close()
			if syncErr != nil {
				return syncErr
			}
		}
		report := describeReviewProcess(ctx, child, err, started)
		if saveErr := saveReviewJSON(filepath.Join(filepath.Dir(path), "child-diagnostic.json"), report); saveErr != nil {
			return fmt.Errorf("could not retain private review process diagnostic")
		}
		if err != nil {
			return fmt.Errorf("review child stopped (%s)", report.Category)
		}
		return nil
	}}
}
