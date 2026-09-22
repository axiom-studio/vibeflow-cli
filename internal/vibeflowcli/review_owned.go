package vibeflowcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errReviewOwnedBusy = errors.New("this review runner is already active; this CLI has not taken ownership")
var errReviewOwnedStopped = errors.New("review runner stopped; check provider credentials and repository binding")

type reviewOwnedSpec struct {
	Config   *Config            `json:"config"`
	Options  reviewWatchOptions `json:"options"`
	Capacity *reviewCapacity    `json:"capacity,omitempty"`
}

type reviewOwnedEvent struct {
	Ready  bool    `json:"ready,omitempty"`
	Error  string  `json:"error,omitempty"`
	Status *string `json:"status,omitempty"`
}

// Only this process holds the pipe's writer. Closing it cannot signal a runner
// started by another CLI, and OS cleanup also closes it after a crash/SIGKILL.
type reviewOwnedRunner struct {
	input    io.WriteCloser
	done     chan struct{}
	once     sync.Once
	err      error // Written before done closes; read only after receiving done.
	statusMu sync.Mutex
	status   string
}

func (r *reviewOwnedRunner) Status() string {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.status
}

func (r *reviewOwnedRunner) Done() <-chan struct{} { return r.done }

func (r *reviewOwnedRunner) Err() error {
	select {
	case <-r.done:
		return r.err
	default:
		return nil
	}
}

func (r *reviewOwnedRunner) Close() error {
	r.once.Do(func() { _ = r.input.Close() })
	select {
	case <-r.done:
		return r.err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("review runner is still finishing shutdown; its pending receipt is preserved")
	}
}

func validateReviewOwned(cfg *Config, o reviewWatchOptions) error {
	if cfg == nil || cfg.APIToken == "" || cfg.ServerURL == "" {
		return fmt.Errorf("connect VibeFlow before starting a review runner")
	}
	if o.ProjectID <= 0 || o.RepositoryLinkID <= 0 || !filepath.IsAbs(o.Repository) || (o.GitProvider != "github" && o.GitProvider != "bitbucket") || (o.Kind != "local" && o.Kind != "shared") {
		return fmt.Errorf("select a project and linked repository before starting a review runner")
	}
	if (o.Provider != "claude" && o.Provider != "codex") || strings.TrimSpace(o.Name) == "" || len(o.Name) > 100 || len(o.Model) > 200 || strings.ContainsAny(o.Name+o.Model, "\x00\r\n\t") || o.Once || o.PollInterval < time.Second || o.PollInterval > time.Minute || o.Timeout < time.Minute || o.Timeout > time.Hour {
		return fmt.Errorf("invalid review runner provider, name, model, or timing")
	}
	return nil
}

// The selected in-memory config, including explicit origin/credential
// overrides, crosses an anonymous pipe. configPath is deliberately not reloaded:
// TUI setup already selected these values, and no consent or secret is saved.
func startReviewOwned(ctx context.Context, cfg *Config, configPath string, options reviewWatchOptions) (*reviewOwnedRunner, error) {
	return startReviewOwnedWithCapacity(ctx, cfg, configPath, options, nil)
}

func startReviewOwnedWithCapacity(ctx context.Context, cfg *Config, _ string, options reviewWatchOptions, capacity *reviewCapacity) (*reviewOwnedRunner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateReviewOwned(cfg, options); err != nil {
		return nil, err
	}
	provider, ok := cfg.Providers[options.Provider]
	if !ok {
		return nil, fmt.Errorf("selected review provider is not configured")
	}
	binary, err := exec.LookPath(provider.Binary)
	if err != nil {
		return nil, fmt.Errorf("selected review provider is not installed")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("could not resolve review provider")
	}
	selected := Provider{Binary: binary}
	modelEnv := map[string]string{}
	keys := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}
	if options.Provider == "codex" {
		keys = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"}
	}
	for _, key := range keys {
		if value := reviewModelEnv(cfg, provider, key); value != "" {
			modelEnv[key] = value
		}
	}
	reviewCfg := &Config{ServerURL: cfg.ServerURL, APIToken: cfg.APIToken, LLMGatewayEnabled: cfg.LLMGatewayEnabled, Providers: map[string]Provider{options.Provider: selected}, SavedEnvVars: modelEnv}
	payload, err := json.Marshal(reviewOwnedSpec{Config: reviewCfg, Options: options, Capacity: capacity})
	if err != nil || len(payload) > 128<<10 {
		return nil, fmt.Errorf("review runner configuration is too large")
	}
	root, err := filepath.Abs(RootDir())
	if err != nil {
		return nil, fmt.Errorf("could not resolve review runner root")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("could not create review runner root")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("could not locate review runner executable")
	}
	cmd := exec.Command(exe, "--root", root, "review-watch", "--owned-runner")
	cmd.Dir = root
	cmd.Env = []string{} // A sparse parent must never turn an empty allowlist into inheritance.
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "CODEX_HOME", "SSH_AUTH_SOCK"} {
		if value := os.Getenv(key); value != "" {
			switch key {
			case "HOME", "TMPDIR", "CODEX_HOME", "SSH_AUTH_SOCK":
				value, err = filepath.Abs(value)
				if err != nil {
					return nil, fmt.Errorf("could not resolve review runner environment paths")
				}
			}
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("could not create review runner ownership pipe")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, fmt.Errorf("could not create review runner status pipe")
	}
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		input.Close()
		stdout.Close()
		return nil, fmt.Errorf("could not start review runner")
	}
	runner := &reviewOwnedRunner{input: input, done: make(chan struct{})}
	ready := make(chan struct{}, 1)
	go func() {
		var terminal error
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024), 8192)
		for scanner.Scan() {
			var event reviewOwnedEvent
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				terminal = errReviewOwnedStopped
				continue
			}
			if event.Ready {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			if event.Status != nil {
				runner.statusMu.Lock()
				runner.status = *event.Status
				runner.statusMu.Unlock()
			}
			if event.Error == "busy" {
				terminal = errReviewOwnedBusy
			} else if event.Error != "" {
				terminal = errReviewOwnedStopped
			}
		}
		if err := cmd.Wait(); err != nil && terminal == nil {
			terminal = errReviewOwnedStopped
		}
		if scanner.Err() != nil {
			terminal = errReviewOwnedStopped
		}
		runner.err = terminal
		close(runner.done)
		runner.once.Do(func() { _ = input.Close() })
	}()
	// Install lifetime cancellation before writing setup or waiting for readiness.
	// A cancelled TUI command may never deliver its returned handle to the UI.
	go func() {
		select {
		case <-ctx.Done():
			_ = runner.Close()
		case <-runner.done:
		}
	}()
	if _, err := input.Write(append(payload, '\n')); err != nil {
		_ = runner.Close()
		return nil, errReviewOwnedStopped
	}
	select {
	case <-ready:
		if ctx.Err() != nil {
			_ = runner.Close()
			return nil, ctx.Err()
		}
		select {
		case <-runner.done:
			if runner.Err() != nil {
				return nil, runner.Err()
			}
			return nil, errReviewOwnedStopped
		default:
		}
		return runner, nil
	case <-runner.done:
		if runner.Err() != nil {
			return nil, runner.Err()
		}
		return nil, errReviewOwnedStopped
	case <-ctx.Done():
		_ = runner.Close()
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		_ = runner.Close()
		return nil, fmt.Errorf("review runner startup timed out; shutdown requested")
	}
}

func runReviewOwned(parent context.Context, input io.Reader, output io.Writer) error {
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	if closer, ok := input.(io.Closer); ok {
		defer closer.Close()
	}
	type setup struct {
		spec reviewOwnedSpec
		err  error
	}
	configured := make(chan setup, 1)
	go func() {
		decoder := json.NewDecoder(io.LimitReader(input, 128<<10))
		decoder.DisallowUnknownFields()
		var spec reviewOwnedSpec
		err := decoder.Decode(&spec)
		configured <- setup{spec: spec, err: err}
		if err == nil {
			_, _ = io.Copy(io.Discard, io.MultiReader(decoder.Buffered(), input))
		}
		cancel()
	}()
	var spec reviewOwnedSpec
	select {
	case setup := <-configured:
		if setup.err != nil {
			return fmt.Errorf("review runner owner configuration is invalid")
		}
		spec = setup.spec
	case <-ctx.Done():
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := validateReviewOwned(spec.Config, spec.Options); err != nil {
		return err
	}
	if spec.Capacity != nil {
		root, err := filepath.Abs(RootDir())
		if err != nil || filepath.Dir(spec.Capacity.Directory) != root {
			return fmt.Errorf("invalid review capacity root")
		}
		if err := spec.Capacity.validate(); err != nil {
			return err
		}
	}
	watch := &reviewWatch{client: NewClient(spec.Config.ServerURL, spec.Config.APIToken), cfg: spec.Config, options: spec.Options, capacity: spec.Capacity, output: io.Discard}
	watch.onReady = func() {
		if json.NewEncoder(output).Encode(reviewOwnedEvent{Ready: true}) != nil {
			cancel()
		}
	}
	watch.onStatus = func(status string) {
		if json.NewEncoder(output).Encode(reviewOwnedEvent{Status: &status}) != nil {
			cancel()
		}
	}
	err := watch.run(ctx)
	if err != nil && ctx.Err() == nil {
		category := "stopped"
		if err.Error() == "review runner or child is already active" {
			category = "busy"
		}
		_ = json.NewEncoder(output).Encode(reviewOwnedEvent{Error: category})
		return errReviewOwnedStopped
	}
	return nil
}
