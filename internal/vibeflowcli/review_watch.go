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

// Idle status for an approved runner whose server lacks repository routing.
const reviewLegacyRoutingNotice = "Repository-request routing requires a server upgrade; running with legacy review routing."

type reviewWatch struct {
	repositoryRequests bool // Registered with repository_review_v1 for this run.
	client             *Client
	cfg                *Config
	options            reviewWatchOptions
	root               string
	state              reviewRunnerState
	output             io.Writer
	providerReady      bool
	onReady            func()
	onStatus           func(string)
	harnessFatal       error // Set when the harness cannot run on this machine until the user acts.
	capacity           *reviewCapacity
	slot               *os.File
	tty                *reviewTerminal // Set when the harness runs interactively in this terminal.
	interrupted        bool            // The user closed the interactive harness before its result.
	stopBrowse         func()          // Ends the idle key handling of tty.
}

// veraBrowseHint tells the user how to reach the history pane from the listener.
const veraBrowseHint = "↑/↓ browse reviews · Enter opens a review · Ctrl-C stops Vera"

// browse turns the idle mode of a listener's terminal on or off: no echo, and
// browse keys go to the history pane beside it.
func (w *reviewWatch) browse(on bool) {
	if w.tty == nil {
		return
	}
	if w.stopBrowse != nil {
		w.stopBrowse()
		w.stopBrowse = nil
	}
	if on {
		pane := os.Getenv("TMUX_PANE")
		w.stopBrowse = w.tty.browse(func(input []byte) { forwardVeraKeys(execVeraTmux, pane, input) })
	}
}

// listening says the runner is idle again.
func (w *reviewWatch) listening() {
	fmt.Fprintln(w.output, reviewListeningLine)
	if w.tty != nil {
		fmt.Fprintln(w.output, veraBrowseHint)
	}
}

// An interactive harness that exits sooner than this without a result could
// not start its review (a crash, or a login or trust screen answered no).
const reviewQuickExit = 15 * time.Second

// reviewWaitNotice is when a waiting interactive review points at the pane.
const reviewWaitNotice = 60 * time.Second

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
	var background, status, history bool
	var stop, managed, serverURL, detail string
	cmd := &cobra.Command{Use: "review-watch", Short: "Run fresh PR reviews in disposable worktrees while this runner is online", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		// Flags parsed; runtime failures print their message, not the usage text.
		cmd.SilenceUsage, cmd.SilenceErrors = true, true // main prints the error once.
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
		if history || detail != "" {
			// The read-only list beside a Vera listener, or one review from
			// it; neither registers.
			o.ProjectID, err = strconv.ParseInt(o.Project, 10, 64)
			if err != nil || o.ProjectID <= 0 || o.RepositoryLinkID <= 0 || cfg.APIToken == "" {
				return fmt.Errorf("review history needs a numeric --project, --repository-link, and a connected config")
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer cancel()
			if detail != "" {
				return runReviewDetail(ctx, cfg, o.ProjectID, detail)
			}
			return runReviewHistory(ctx, cfg, o)
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
		if _, _, err := reviewHarnessArgs(o.Provider, "", ""); err != nil {
			return err
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
		// Every foreground listener on this machine and root, such as each Vera
		// tmux session, shares one review_concurrency limit.
		capacity, err := sharedReviewCapacity(RootDir(), cfg.ReviewConcurrency)
		if err != nil {
			return err
		}
		// A terminal (a Vera tmux pane) shows each harness's interactive UI.
		watch := &reviewWatch{client: client, cfg: cfg, options: o, output: cmd.OutOrStdout(), capacity: capacity, tty: openReviewTerminal(cmd.OutOrStdout())}
		return watch.run(ctx)
	}}
	cmd.Flags().StringVar(&o.Project, "project", "", "VibeFlow project name or ID")
	cmd.Flags().StringVar(&o.Repository, "repo", "", "Local checkout for the linked repository (default: current directory)")
	cmd.Flags().Int64Var(&o.RepositoryLinkID, "repository-link", 0, "VibeFlow repository link ID")
	cmd.Flags().StringVar(&o.GitProvider, "git-provider", "github", "Repository integration: github or bitbucket")
	cmd.Flags().StringVar(&o.Provider, "provider", "", "Coding harness for Vera: "+strings.Join(reviewHarnessKeys, ", ")+" (default: configured provider); it runs with your normal login and full permissions in a disposable worktree")
	cmd.Flags().StringVar(&o.Model, "model", "", "Model selection; required for gateway reviews")
	cmd.Flags().StringVar(&o.Kind, "runner-kind", "local", "local or explicitly authorized shared runner")
	cmd.Flags().StringVar(&o.Name, "name", "", "Runner name (default: hostname)")
	cmd.Flags().DurationVar(&o.PollInterval, "interval", o.PollInterval, "Idle API polling interval, 1s to 60s; each poll also heartbeats the runner, no LLM runs while idle")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", o.Timeout, "Maximum time per attempt, also bounded by the server deadline")
	cmd.Flags().BoolVar(&history, "history", false, "Show this repository's reviews as a scrollable read-only list (Vera's right-hand pane)")
	_ = cmd.Flags().MarkHidden("history")
	cmd.Flags().StringVar(&detail, "review-detail", "", "Show one review's recorded result and findings (opened from Vera's history pane)")
	_ = cmd.Flags().MarkHidden("review-detail")
	cmd.Flags().BoolVar(&o.Once, "once", false, "Check one page of available work and exit after at most one review")
	cmd.Flags().BoolVar(&background, "background", false, "Run this explicit binding in the background independently of the TUI")
	cmd.Flags().BoolVar(&status, "status", false, "Show managed review runners in this root")
	cmd.Flags().StringVar(&stop, "stop", "", "Stop and disable a detached runner ID")
	cmd.Flags().StringVar(&managed, "managed-runner", "", "Internal managed runner binding ID")
	cmd.Flags().StringVar(&serverURL, "server-url", "", "VibeFlow server URL (pinned for background runners)")
	_ = cmd.Flags().MarkHidden("managed-runner")
	cmd.MarkFlagsMutuallyExclusive("background", "status", "stop", "managed-runner")
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
	registration := map[string]any{"id": w.state.ID, "kind": w.options.Kind, "name": w.options.Name, "provider": w.options.GitProvider, "repository_link_id": w.options.RepositoryLinkID}
	idleStatus := "" // Shown while healthy, so owned and detached runners surface it too.
	if w.options.GitProvider == "github" {
		var discovery reviewStartupRepositoriesResponse
		if err = w.client.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-repositories", w.options.ProjectID), nil, &discovery); err != nil {
			return err
		}
		supported := false
		for _, capability := range discovery.SupportedRunnerCapabilities {
			if capability == "repository_review_v1" {
				supported = true
			}
		}
		if supported {
			registration["capabilities"] = []string{"repository_review_v1"}
		} else {
			idleStatus = reviewLegacyRoutingNotice
		}
	}
	if err = w.client.reviewRequest(ctx, "POST", fmt.Sprintf("/projects/%d/pr-review-runners", w.options.ProjectID), registration, &registered); err != nil {
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
	if _, ok := registration["capabilities"]; ok {
		w.repositoryRequests = true
		fmt.Fprintln(w.output, "Repository requests enabled: anyone who comments @vibeflow review on this linked repository can request a review from this runner.")
	}
	defer func() {
		if w.state.Pending == nil {
			stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			w.client.reviewRequest(stopCtx, "DELETE", w.prefix(), nil, nil)
		}
	}()
	stopHint := "Ctrl-C stops it."
	if w.tty != nil {
		stopHint = "Ctrl-C stops it while listening; during a review Ctrl-C goes to the harness, and Vera then offers to stop."
	}
	fmt.Fprintf(w.output, "Review runner %s is online (%s, %s). %s\n", w.options.Name, w.options.Kind, w.options.Provider, stopHint)
	w.listening()
	w.browse(true)
	defer w.browse(false)
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
	unavailable := false // One line when an outage starts and one when it ends.
	status := idleStatus
	if status != "" {
		if w.onStatus != nil {
			w.onStatus(status)
		}
		fmt.Fprintln(w.output, status)
	}
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
			nextStatus = idleStatus
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
			if !unavailable {
				unavailable = true
				fmt.Fprintln(w.output, "Review API unavailable; retrying with backoff up to 1m. The pending receipt is safe.")
			}
		} else {
			if unavailable {
				unavailable = false
				fmt.Fprintln(w.output, "Review API reachable again.")
			}
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
		// Headless, a logged-out harness fails fast with a classified message;
		// interactively it would wait on a login screen until the deadline.
		if w.tty != nil {
			if err := checkReviewHarnessLogin(ctx, w.cfg, w.options.Provider); err != nil {
				return err
			}
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
		fmt.Fprintf(w.output, "\nClaimed %s at %.12s; starting a fresh Vera with %s.\n", reviewPRLabel(p.Execution.Review), p.Execution.Attempt.Round.HeadSHA, w.options.Provider)
		result, err := w.execute(ctx, p)
		if err != nil {
			p.Failure = err.Error()
		} else {
			p.Result = result
		}
		if err = w.save(); err != nil {
			return err
		}
		fmt.Fprintln(w.output, reviewOutcomeLine(p))
	}
	// Cleanup precedes network publication, and the exact submission remains
	// durable even if a successful server response is lost.
	if err := w.cleanup(p); err != nil {
		return err
	}
	fmt.Fprintln(w.output, "Review worktree removed.")
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
		fmt.Fprintln(w.output, "Failure reported to VibeFlow.")
	} else {
		fmt.Fprintln(w.output, "Result sent to VibeFlow; the server handles tickets and PR publication.")
	}
	p.Completed = true
	if err := w.finishReceipt(p); err != nil {
		return err
	}
	// Retrying would fail the same way and spend the review's attempts.
	if fatal := w.harnessFatal; fatal != nil {
		w.harnessFatal = nil
		return fatal
	}
	if ctx.Err() != nil {
		return nil // Stopping: no longer listening.
	}
	if w.interrupted {
		w.interrupted = false
		fmt.Fprintln(w.output, "Review interrupted. Press Ctrl-C again within 5 s to stop Vera, or wait to resume listening.")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
	w.listening()
	return nil
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
	if w.tty != nil {
		// Before the directory goes, while its real path still resolves.
		_ = forgetReviewTrust(w.options.Provider, reviewProviderEnv(w.cfg, w.options.Provider), dir)
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
			return removeReviewDir(dir)
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
			if err == nil {
				err = w.client.reviewRequest(callCtx, "POST", w.attemptPath(p)+"/renew", reviewRenewBody(*p.Execution, milestones&1 != 0, milestones&2 != 0), &renewed)
			}
			stop()
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
	fmt.Fprintf(w.output, "Review worktree ready: %s\n", filepath.Join(root, "input", "head"))
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
	if w.cfg.LLMGatewayEnabled && w.options.Provider == "claude" {
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
	interactive := w.tty != nil
	if interactive {
		if err = makeReviewSpecInteractive(spec, w.cfg, w.options.Provider, w.options.Model, root); err != nil {
			return nil, err
		}
	}
	var extra []*os.File // The guard's descriptors from 3 on.
	if w.slot != nil {
		extra = append(extra, w.slot)
		spec.CapacityFD = 3
		spec.Cleanup = &reviewProviderCleanup{Reservation: *p.Capacity, RequestID: p.RequestID, JobID: p.JobID, AttemptID: p.Execution.Attempt.ID}
	}
	if interactive {
		spec.TerminalFD = 3 + len(extra)
		extra = append(extra, w.tty.files[:]...)
	}
	if err = saveReviewJSON(filepath.Join(root, "child.json"), spec); err != nil {
		return nil, err
	}
	executable, err := cliExecutable()
	if err != nil {
		return nil, err
	}
	guard := exec.Command(executable, "review-child", filepath.Join(root, "child.json"))
	guard.ExtraFiles = extra
	setStage("child_guard")
	guard.WaitDelay = 250 * time.Millisecond
	guard.Env = []string{"PATH=" + os.Getenv("PATH")}
	guard.Dir = root
	// Headless harness output is counted and only its tail is kept, in memory,
	// to name failures the user must fix (login, trust); it is never relayed
	// or sent to the server. An interactive harness owns the terminal instead.
	stdout, stderr := reviewByteCounter{limit: 8 << 20}, reviewByteCounter{limit: 64 << 10}
	guard.Stdout = &stdout
	guard.Stderr = &stderr
	if interactive {
		fmt.Fprintf(w.output, "Starting %s in this pane; Vera closes it once the review result is written.\n", w.options.Provider)
		// The harness gets the terminal as it was, and Vera's idle mode back
		// once the harness is gone.
		w.browse(false)
		defer w.browse(true)
		w.tty.save()
	} else {
		fmt.Fprintf(w.output, "Running %s headless; its output is not shown.\n", w.options.Provider)
	}
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
		if interactive {
			w.tty.reclaim(false)
		}
		return nil, fmt.Errorf("review child guard failed")
	}
	resultPath := filepath.Join(root, "result.json")
	var written <-chan struct{} // An interactive harness stays open after writing its result.
	if interactive {
		written = watchReviewResult(ctx, resultPath, p, brief.Digest)
	}
	harnessStarted := time.Now()
	done := make(chan error, 1)
	go func() { done <- guard.Wait() }()
	notice := time.Duration(0) // A headless harness has no screen to answer.
	if interactive {
		notice = reviewWaitNotice
	}
	finished, err := waitReviewHarness(ctx, done, written, notice, func() { pipe.Close() }, func() {
		fmt.Fprintf(w.output, "\r\nVera is still waiting for %s; if it is showing a login or trust screen, answer it here or press Ctrl-C twice to stop Vera.\r\n", w.options.Provider)
	})
	ran := time.Since(harnessStarted)
	diagnostic.StdoutBytes, diagnostic.StderrBytes = stdout.n, stderr.n
	if report, ok := readReviewProcessReport(filepath.Join(root, "child-diagnostic.json")); ok {
		diagnostic.Stage = "provider"
		stageStarted = time.Now().Add(-time.Duration(report.DurationMS) * time.Millisecond)
		diagnostic.ExitCode, diagnostic.Signal = report.ExitCode, report.Signal
		diagnostic.ProviderDurationMS = report.DurationMS
		if report.Category != "completed" && !finished {
			diagnostic.Category = report.Category
		}
	} else if guard.ProcessState != nil && !finished {
		code := guard.ProcessState.ExitCode()
		diagnostic.ExitCode = &code
		diagnostic.Signal = reviewProcessSignal(guard.ProcessState)
		diagnostic.Category = "child_guard_failed"
	}
	if interactive {
		// The guard stops the harness with SIGINT, then SIGTERM and SIGKILL.
		w.tty.reclaim(diagnostic.Signal == int(syscall.SIGTERM) || diagnostic.Signal == int(syscall.SIGKILL))
		if _, _, resultErr := readReviewResult(resultPath, p, brief.Digest); !finished && ctx.Err() == nil && resultErr != nil {
			// Only the user closes an interactive harness early, unless it
			// could not start its review at all.
			if ran < reviewQuickExit && diagnostic.Signal != int(syscall.SIGINT) {
				exit := ""
				if diagnostic.ExitCode != nil && *diagnostic.ExitCode >= 0 {
					exit = fmt.Sprintf(" (exit code %d)", *diagnostic.ExitCode)
				}
				w.harnessFatal = fmt.Errorf("Vera stopped: %s exited within %d s without a review%s; it may need a login or a trust answer (%s). The review stays queued; start Vera again when %s works", w.options.Provider, int(reviewQuickExit.Seconds()), exit, reviewHarnessLoginHint(w.options.Provider), w.options.Provider)
			} else {
				w.interrupted = true
				diagnostic.Category = "cancelled"
			}
			return nil, diagnostic.failure()
		}
	}
	if category := classifyReviewHarnessOutput(string(stdout.tail) + "\n" + string(stderr.tail)); category != "" && ctx.Err() == nil && (err != nil || !reviewFileExists(resultPath)) {
		diagnostic.Category = category
		w.harnessFatal = fmt.Errorf("Vera stopped: the %s harness %s on this machine; %s, then start Vera again", w.options.Provider, reviewHarnessProblem(category), reviewHarnessLoginHint(w.options.Provider))
		return nil, diagnostic.failure()
	}
	if err != nil || ctx.Err() != nil {
		return nil, diagnostic.failure()
	}
	setStage("result")
	diagnostic.Category = "invalid_result"
	// Every harness writes its result to the same file; a missing file after
	// exit is an invalid result.
	result, reported, err := readReviewResult(resultPath, p, brief.Digest)
	if err != nil {
		return nil, err
	}
	if result == nil {
		if reported {
			diagnostic.Category = "provider_reported_failure"
		}
		return nil, diagnostic.failure()
	}
	markProgress(2)
	return result, nil
}

// readReviewResult reads a harness's result.json. A complete envelope returns
// no error: result is set for a result matching the claimed revision and
// brief, and reported marks an explicit failure_reason instead.
func readReviewResult(path string, p *reviewReceipt, digest string) (result json.RawMessage, reported bool, err error) {
	output, err := reviewReadBounded(path, 256<<10)
	if err != nil {
		return nil, false, fmt.Errorf("review harness wrote no bounded result file")
	}
	var envelope struct {
		Result  json.RawMessage `json:"result"`
		Failure string          `json:"failure_reason"`
	}
	if json.Unmarshal(output, &envelope) != nil {
		return nil, false, fmt.Errorf("review provider returned invalid JSON")
	}
	if envelope.Failure != "" || len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return nil, envelope.Failure != "", nil
	}
	var identity struct {
		Version int    `json:"schema_version"`
		Head    string `json:"head_sha"`
		Base    string `json:"base_sha"`
		Digest  string `json:"brief_digest"`
	}
	if json.Unmarshal(envelope.Result, &identity) != nil || identity.Version != 1 || identity.Head != p.Execution.Attempt.Round.HeadSHA || identity.Base != p.Execution.Attempt.Round.BaseSHA || identity.Digest != digest {
		return nil, false, fmt.Errorf("review result does not match the claimed revision and brief")
	}
	return envelope.Result, false, nil
}

// watchReviewResult closes its channel once result.json holds a complete
// envelope for this attempt; a partly written file is read again later.
func watchReviewResult(ctx context.Context, path string, p *reviewReceipt, digest string) <-chan struct{} {
	written := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			if _, _, err := readReviewResult(path, p, digest); err == nil {
				close(written)
				return
			}
		}
	}()
	return written
}

func reviewChildCmd() *cobra.Command {
	return &cobra.Command{Use: "review-child <private-spec>", Hidden: true, Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
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
		child := exec.Command(spec.Binary, spec.Args...)
		child.Dir = spec.Dir
		child.Env = spec.Env
		if spec.Interactive {
			// The harness's own UI, in the foreground of the runner's terminal.
			tty, err := reviewTerminalFiles(spec.TerminalFD)
			if err != nil {
				return err
			}
			for _, f := range tty {
				defer f.Close()
			}
			child.Stdin, child.Stdout, child.Stderr = tty[0], tty[1], tty[2]
			child.SysProcAttr = reviewForegroundAttr(tty[0])
		} else {
			input, err := os.Open(spec.InputFile)
			if err != nil {
				return err
			}
			defer input.Close()
			child.Stdin = input
			child.Stdout = cmd.OutOrStdout()
			child.Stderr = cmd.ErrOrStderr()
		}
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

type reviewByteCounter struct {
	n, limit int
	tail     []byte // Last 8 KiB, for local failure classification only.
}

func (c *reviewByteCounter) Write(p []byte) (int, error) {
	c.n = min(c.n+len(p), c.limit)
	c.tail = append(c.tail, p...)
	if len(c.tail) > 8<<10 {
		c.tail = append([]byte(nil), c.tail[len(c.tail)-8<<10:]...)
	}
	return len(p), nil
}

func reviewFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// classifyReviewHarnessOutput names failures that need the user, not a retry.
func classifyReviewHarnessOutput(output string) string {
	text := strings.ToLower(output)
	for _, marker := range []string{"not running in a trusted directory", "workspace is not trusted", "folder is not trusted", "trust this folder"} {
		if strings.Contains(text, marker) {
			return "untrusted_workspace"
		}
	}
	for _, marker := range []string{"not logged in", "authentication required", "please run 'agent login'", "please log in", "please login", "invalid access token", "token expired", "invalid api key", "invalid_api_key", "unauthorized", "401", "login required", "no credentials"} {
		if strings.Contains(text, marker) {
			return "authentication_required"
		}
	}
	for _, marker := range []string{"ineligibletiererror", "no longer supported", "403", "access denied", "forbidden"} {
		if strings.Contains(text, marker) {
			return "access_denied"
		}
	}
	return ""
}

func reviewHarnessProblem(category string) string {
	switch category {
	case "untrusted_workspace":
		return "refused the review worktree as untrusted"
	case "access_denied":
		return "was refused access by its model service"
	}
	return "is not logged in"
}

func reviewHarnessLoginHint(provider string) string {
	switch provider {
	case "claude":
		return "run claude and use /login"
	case "codex":
		return "run codex login"
	case "gemini":
		return "run gemini to sign in, or set GEMINI_API_KEY"
	case "qwen":
		return "run qwen and sign in again, or refresh its API key"
	case "copilot":
		return "run copilot and use /login"
	case "cursor":
		return "run agent login"
	case "kiro":
		return "run kiro-cli login"
	}
	return "sign in to the harness"
}

func reviewReadBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("review result exceeds size limit")
	}
	return data, err
}

// removeReviewDir deletes a finished review, including its worktree. Tools run
// by the harness may leave read-only directories, so it retries after making
// every directory writable.
func removeReviewDir(dir string) error {
	if os.RemoveAll(dir) == nil {
		return nil
	}
	filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			os.Chmod(path, 0700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

const reviewListeningLine = "Listening for @vibeflow review requests; no model runs while idle."

func reviewPRLabel(job reviewJob) string {
	if job.Number > 0 {
		return fmt.Sprintf("PR #%d", job.Number)
	}
	return "review " + job.ID
}

// reviewOutcomeLine summarizes a finished attempt for the runner's own output.
func reviewOutcomeLine(p *reviewReceipt) string {
	if p.Failure != "" {
		return "Result: failed - " + p.Failure
	}
	var result struct {
		Outcome  string            `json:"outcome"`
		Findings []json.RawMessage `json:"new_findings"`
	}
	_ = json.Unmarshal(p.Result, &result)
	if result.Outcome == "changes_requested" {
		return fmt.Sprintf("Result: changes requested with %d new findings.", len(result.Findings))
	}
	return "Result: clean."
}

// waitReviewHarness waits until the guard exits, the attempt ends, or an
// interactive harness has written its result, which stop then closes. After
// notice (when set) it calls say once. finished reports a written result.
func waitReviewHarness(ctx context.Context, done <-chan error, written <-chan struct{}, notice time.Duration, stop, say func()) (finished bool, err error) {
	var noticed <-chan time.Time
	if notice > 0 {
		timer := time.NewTimer(notice)
		defer timer.Stop()
		noticed = timer.C
	}
	for {
		select {
		case err = <-done:
			stop()
			return false, err
		case <-ctx.Done():
			stop()
			return false, <-done
		case <-written:
			stop() // The guard stops the harness: SIGINT, SIGTERM, then SIGKILL.
			<-done
			return true, nil
		case <-noticed:
			noticed = nil
			say()
		}
	}
}
