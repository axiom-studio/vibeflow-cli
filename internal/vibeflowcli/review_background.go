package vibeflowcli

import (
	"context"
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
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// The opt-in binding stores credential sources and a one-way account guard.
// Secrets stay in the user's config or existing model login store.
type reviewBackground struct {
	Enabled        bool               `json:"enabled"`
	ConfigPath     string             `json:"config_path"`
	Root           string             `json:"root"`
	ServerURL      string             `json:"server_url"`
	CredentialHash string             `json:"credential_hash"`
	ProviderBinary string             `json:"provider_binary"`
	Home           string             `json:"home"`
	CodexHome      string             `json:"codex_home,omitempty"`
	User           string             `json:"user,omitempty"`
	Logname        string             `json:"logname,omitempty"`
	SSHAgentSocket string             `json:"ssh_agent_socket,omitempty"`
	Options        reviewWatchOptions `json:"options"`
}

type reviewBackgroundStatus struct {
	PID    int    `json:"pid"`
	Phase  string `json:"phase"`
	Reason string `json:"reason,omitempty"`
}

// Unlike LoadConfig, managing a runner neither migrates the file nor accepts
// ambient account overrides. Malformed YAML must not echo credential values.
func loadReviewBackgroundConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("managed review runner requires a readable config file")
	}
	cfg := DefaultConfig()
	if yaml.Unmarshal(data, cfg) != nil {
		return nil, fmt.Errorf("managed review runner config is invalid")
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, fmt.Errorf("save VibeFlow credentials in the selected config before enabling a background runner")
	}
	return cfg, nil
}

func reviewBackgroundHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func reviewBackgroundID(serverURL string, o reviewWatchOptions) string {
	identity := fmt.Sprintf("%s\n%d\n%d\n%s\n%s\n%s", serverURL, o.ProjectID, o.RepositoryLinkID, o.GitProvider, o.Kind, o.Name)
	return reviewBackgroundHash(identity)[:32]
}

func reviewBackgroundDir(id string) (string, error) {
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 || strings.ToLower(id) != id {
		return "", fmt.Errorf("invalid managed runner ID; use review-watch --status")
	}
	root, err := filepath.Abs(RootDir())
	if err != nil {
		return "", fmt.Errorf("could not resolve review runner root")
	}
	return filepath.Join(root, "review-runners", id), nil
}

func readReviewBackground(dir string) (*reviewBackground, error) {
	data, err := os.ReadFile(filepath.Join(dir, "background.json"))
	var binding reviewBackground
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &binding) != nil {
		return nil, fmt.Errorf("managed runner binding is missing or invalid")
	}
	if binding.Root != filepath.Dir(filepath.Dir(dir)) || reviewBackgroundID(binding.ServerURL, binding.Options) != filepath.Base(dir) || !filepath.IsAbs(binding.ConfigPath) || !filepath.IsAbs(binding.Options.Repository) {
		return nil, fmt.Errorf("managed runner binding changed; explicitly enable it again")
	}
	return &binding, nil
}

func reviewBackgroundActive(dir, name string) (bool, error) {
	lock, err := lockReviewFile(filepath.Join(dir, name))
	if err == nil {
		lock.Close()
		return false, nil
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return false, fmt.Errorf("could not inspect managed runner lock")
	}
	if err.Error() == "review runner or child is already active" {
		return true, nil
	}
	return false, fmt.Errorf("background review runners require macOS or Linux")
}

func lockReviewBackgroundControl(ctx context.Context, dir string) (*os.File, error) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		lock, err := lockReviewFile(filepath.Join(dir, "background-control.lock"))
		if err == nil {
			return lock, nil
		}
		if err.Error() != "review runner or child is already active" {
			return nil, fmt.Errorf("could not lock managed runner controls")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("another runner control is busy; retry review-watch --status")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func startReviewBackground(ctx context.Context, cfg *Config, configPath string, o reviewWatchOptions, out io.Writer) error {
	if o.Once {
		return fmt.Errorf("--once cannot be used with --background")
	}
	fileCfg, err := loadReviewBackgroundConfig(configPath)
	if err != nil {
		return err
	}
	if fileCfg.APIToken != cfg.APIToken {
		return fmt.Errorf("background runner credentials must come from the selected config file")
	}
	origin, err := url.Parse(cfg.ServerURL)
	if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return fmt.Errorf("managed runner server URL must be an HTTP origin without credentials or query parameters")
	}
	// Ambient-only credentials and interpolated secrets cannot survive a later
	// TUI launch without silently choosing that new shell's account.
	keys := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}
	if o.Provider == "codex" {
		keys = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"}
	}
	if cfg.LLMGatewayEnabled {
		keys = nil
	}
	for _, key := range keys {
		value := cfg.Providers[o.Provider].Env[key]
		if strings.Contains(value, "$") || (value == "" && cfg.SavedEnvVars[key] == "" && os.Getenv(key) != "") {
			return fmt.Errorf("background model credentials and endpoints must be saved as literal provider.env or saved_env_vars values")
		}
	}
	binary, err := exec.LookPath(cfg.Providers[o.Provider].Binary)
	if err != nil {
		return fmt.Errorf("selected review provider is not installed")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return fmt.Errorf("could not resolve review provider")
	}
	root, err := filepath.Abs(RootDir())
	if err != nil {
		return fmt.Errorf("could not resolve review runner root")
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("could not resolve review runner config")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not resolve model login home")
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome != "" {
		codexHome, err = filepath.Abs(codexHome)
		if err != nil {
			return fmt.Errorf("could not resolve Codex login source")
		}
	}
	sshAgent := os.Getenv("SSH_AUTH_SOCK")
	if sshAgent != "" {
		info, err := os.Stat(sshAgent)
		if !filepath.IsAbs(sshAgent) || err != nil || info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("background SSH authentication requires an existing absolute SSH agent socket")
		}
	}
	binding := &reviewBackground{Enabled: true, ConfigPath: configPath, Root: root, ServerURL: cfg.ServerURL, CredentialHash: reviewBackgroundHash(cfg.APIToken), ProviderBinary: binary, Home: home, CodexHome: codexHome, User: os.Getenv("USER"), Logname: os.Getenv("LOGNAME"), SSHAgentSocket: sshAgent, Options: o}
	dir, err := reviewBackgroundDir(reviewBackgroundID(cfg.ServerURL, o))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("could not create managed runner directory")
	}
	lock, err := lockReviewBackgroundControl(ctx, dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	active, err := reviewBackgroundActive(dir, "background.lock")
	if err != nil {
		return err
	}
	if active {
		previous, err := readReviewBackground(dir)
		if err != nil || *previous != *binding {
			return fmt.Errorf("runner is active with a different binding; stop it before enabling changes")
		}
		fmt.Fprintf(out, "Managed review runner %s is already active.\n", filepath.Base(dir))
		return nil
	}
	active, err = reviewBackgroundActive(dir, "runner.lock")
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("this review runner is already active in the foreground")
	}
	if err = saveReviewJSON(filepath.Join(dir, "background.json"), binding); err != nil {
		return fmt.Errorf("could not save managed runner binding")
	}
	return launchReviewBackground(ctx, dir, binding, out)
}

// Caller holds the control lock until the child has acquired its lifetime lock
// and passed its first heartbeat, so concurrent starts cannot launch duplicates.
func launchReviewBackground(ctx context.Context, dir string, binding *reviewBackground, out io.Writer) error {
	if err := os.Remove(filepath.Join(dir, "background.stop")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not clear managed runner stop request")
	}
	if err := saveReviewJSON(filepath.Join(dir, "background-status.json"), &reviewBackgroundStatus{Phase: "starting"}); err != nil {
		return fmt.Errorf("could not save managed runner status")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate review runner executable")
	}
	cmd := exec.Command(exe, "--root", binding.Root, "--config", binding.ConfigPath, "review-watch", "--managed-runner", filepath.Base(dir))
	cmd.Dir = binding.Root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + binding.Home, "USER=" + binding.User, "LOGNAME=" + binding.Logname, "TMPDIR=" + os.TempDir()}
	if binding.CodexHome != "" {
		cmd.Env = append(cmd.Env, "CODEX_HOME="+binding.CodexHome)
	}
	if binding.SSHAgentSocket != "" {
		cmd.Env = append(cmd.Env, "SSH_AUTH_SOCK="+binding.SSHAgentSocket)
	}
	// Child output is discarded. Only fixed lifecycle messages enter its private
	// log; provider output and config parsing errors cannot leak through Cobra.
	setProcessGroup(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("could not start managed review runner")
	}
	go cmd.Wait()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		var status reviewBackgroundStatus
		data, _ := os.ReadFile(filepath.Join(dir, "background-status.json"))
		_ = json.Unmarshal(data, &status)
		active, _ := reviewBackgroundActive(dir, "background.lock")
		if active && status.Phase == "running" {
			fmt.Fprintf(out, "Managed review runner %s is running. Stop: vibeflow --root %s review-watch --stop %s\n", filepath.Base(dir), shellQuote(binding.Root), filepath.Base(dir))
			return nil
		}
		if status.Phase == "failed" {
			return fmt.Errorf("managed review runner failed to start; inspect review-watch --status")
		}
		select {
		case <-ctx.Done():
			binding.Enabled = false
			_ = saveReviewJSON(filepath.Join(dir, "background.json"), binding)
			_ = os.WriteFile(filepath.Join(dir, "background.stop"), nil, 0600)
			return ctx.Err()
		case <-deadline.C:
			binding.Enabled = false
			_ = saveReviewJSON(filepath.Join(dir, "background.json"), binding)
			_ = os.WriteFile(filepath.Join(dir, "background.stop"), nil, 0600)
			return fmt.Errorf("managed review runner startup timed out; stop requested; inspect review-watch --status")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func runReviewBackground(ctx context.Context, id string) error {
	dir, err := reviewBackgroundDir(id)
	if err != nil {
		return err
	}
	lock, err := lockReviewFile(filepath.Join(dir, "background.lock"))
	if err != nil {
		return fmt.Errorf("managed runner is already active or unavailable")
	}
	defer lock.Close()
	status := reviewBackgroundStatus{PID: os.Getpid(), Phase: "failed", Reason: "binding_invalid"}
	defer func() {
		_ = saveReviewJSON(filepath.Join(dir, "background-status.json"), &status)
		if log, err := os.OpenFile(filepath.Join(dir, "background.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			fmt.Fprintf(log, "%s managed review runner %s %s\n", time.Now().UTC().Format(time.RFC3339), status.Phase, status.Reason)
			log.Close()
		}
	}()
	binding, err := readReviewBackground(dir)
	if err != nil {
		return err
	}
	if !binding.Enabled {
		status.Phase = "stopped"
		status.Reason = ""
		return nil
	}
	status.Reason = "config_unavailable"
	cfg, err := loadReviewBackgroundConfig(binding.ConfigPath)
	if err != nil {
		return err
	}
	if reviewBackgroundHash(cfg.APIToken) != binding.CredentialHash {
		status.Reason = "credential_binding_changed"
		return fmt.Errorf("managed runner credential binding changed; explicitly enable it again")
	}
	cfg.ServerURL = binding.ServerURL
	p := cfg.Providers[binding.Options.Provider]
	p.Binary = binding.ProviderBinary
	cfg.Providers[binding.Options.Provider] = p
	status.Reason = "provider_or_api_unavailable"
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(filepath.Join(dir, "background.stop")); err == nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	watch := &reviewWatch{client: NewClient(cfg.ServerURL, cfg.APIToken), cfg: cfg, options: binding.Options, output: io.Discard}
	watch.onReady = func() {
		status.Phase = "running"
		status.Reason = ""
		if saveReviewJSON(filepath.Join(dir, "background-status.json"), &status) != nil {
			cancel()
		}
	}
	err = watch.run(ctx)
	if err == nil {
		status.Phase = "stopped"
		status.Reason = ""
	} else {
		status.Phase = "failed"
		if status.Reason == "" {
			status.Reason = "runner_failed"
		}
	}
	return err
}

func stopReviewBackground(ctx context.Context, id string, out io.Writer) error {
	dir, err := reviewBackgroundDir(id)
	if err != nil {
		return err
	}
	lock, err := lockReviewBackgroundControl(ctx, dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	binding, err := readReviewBackground(dir)
	if err != nil {
		return err
	}
	binding.Enabled = false
	if saveReviewJSON(filepath.Join(dir, "background.json"), binding) != nil {
		return fmt.Errorf("could not disable detached runner")
	}
	if os.WriteFile(filepath.Join(dir, "background.stop"), nil, 0600) != nil {
		return fmt.Errorf("could not request managed runner stop")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		active, err := reviewBackgroundActive(dir, "background.lock")
		if err != nil {
			return err
		}
		if !active {
			fmt.Fprintf(out, "Managed review runner %s stopped; detached runner disabled.\n", id)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			fmt.Fprintf(out, "Managed review runner %s is stopping; detached runner disabled. Pending receipts are preserved.\n", id)
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func reviewBackgroundState(dir string) (string, error) {
	binding, err := readReviewBackground(dir)
	if err != nil {
		return "invalid", err
	}
	active, err := reviewBackgroundActive(dir, "background.lock")
	if err != nil {
		return "unknown", err
	}
	var status reviewBackgroundStatus
	data, readErr := os.ReadFile(filepath.Join(dir, "background-status.json"))
	valid := readErr == nil && len(data) <= 4096 && json.Unmarshal(data, &status) == nil
	if active {
		if !binding.Enabled {
			return "stopping (detached runner disabled)", nil
		}
		if valid && status.Phase == "running" {
			return "running", nil
		}
		return "starting", nil
	}
	if !binding.Enabled {
		return "stopped (detached runner disabled)", nil
	}
	if !valid {
		return "stale (explicit restart required)", nil
	}
	if status.Phase == "failed" {
		switch status.Reason {
		case "binding_invalid", "config_unavailable", "credential_binding_changed", "provider_or_api_unavailable", "runner_failed":
			return "failed: " + status.Reason + " (explicit restart required)", nil
		default:
			return "failed (explicit restart required)", nil
		}
	}
	if status.Phase == "stopped" && status.PID > 0 {
		return "stopped (explicit restart required)", nil
	}
	return "stale (explicit restart required)", nil
}

func reviewBackgroundStatusList(out io.Writer) error {
	root, err := filepath.Abs(RootDir())
	if err != nil {
		return fmt.Errorf("could not resolve managed runner root")
	}
	paths, err := filepath.Glob(filepath.Join(root, "review-runners", "*", "background.json"))
	if err != nil {
		return fmt.Errorf("could not list managed review runners")
	}
	if len(paths) == 0 {
		fmt.Fprintln(out, "No managed review runners. Enable one with review-watch --background and an explicit repository binding.")
	}
	for _, path := range paths {
		dir := filepath.Dir(path)
		state, _ := reviewBackgroundState(dir)
		fmt.Fprintf(out, "%s %s\n", filepath.Base(dir), state)
		if binding, err := readReviewBackground(dir); err == nil {
			fmt.Fprintf(out, "  name=%q project=%d repository-link=%d provider=%q model=%q server=%q\n", binding.Options.Name, binding.Options.ProjectID, binding.Options.RepositoryLinkID, binding.Options.Provider, binding.Options.Model, binding.ServerURL)
		}
		if diagnostic, ok := readReviewExecutionDiagnostic(filepath.Join(dir, "last-provider-diagnostic.json")); ok {
			fmt.Fprintf(out, "  last failed attempt (%s): %v\n", time.UnixMilli(diagnostic.RecordedAt).UTC().Format(time.RFC3339), diagnostic.failure())
		}
		fmt.Fprintf(out, "  stop: vibeflow --root %s review-watch --stop %s\n", shellQuote(root), filepath.Base(dir))
	}
	return nil
}
