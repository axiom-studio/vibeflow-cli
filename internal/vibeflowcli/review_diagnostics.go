package vibeflowcli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	errReviewLeaseExpired  = errors.New("review execution lease expired")
	errReviewLeaseRejected = errors.New("review execution lease renewal rejected")
	errReviewLeaseIdentity = errors.New("review execution lease identity changed")
)

// Never add free-form errors, paths, argv, environment, prompts or provider
// output here. Diagnostics cross the child boundary and survive input cleanup.
type reviewProcessReport struct {
	Version    int    `json:"version"`
	Category   string `json:"category"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Signal     int    `json:"signal,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type reviewExecutionDiagnostic struct {
	Version            int    `json:"version"`
	AttemptID          string `json:"attempt_id"`
	Provider           string `json:"provider"`
	Stage              string `json:"stage"`
	Category           string `json:"category"`
	ExitCode           *int   `json:"exit_code,omitempty"`
	Signal             int    `json:"signal,omitempty"`
	APIErrorStatus     int    `json:"api_error_status,omitempty"`
	DurationMS         int64  `json:"duration_ms"`
	StageDurationMS    int64  `json:"stage_duration_ms"`
	ProviderDurationMS int64  `json:"provider_duration_ms,omitempty"`
	StdoutBytes        int    `json:"stdout_bytes"`
	StderrBytes        int    `json:"stderr_bytes"`
	RecordedAt         int64  `json:"recorded_at"`
}

func reviewCancellationCategory(ctx context.Context) string {
	switch {
	case errors.Is(context.Cause(ctx), errReviewLeaseExpired):
		return "lease_expired"
	case errors.Is(context.Cause(ctx), errReviewLeaseRejected):
		return "lease_rejected"
	case errors.Is(context.Cause(ctx), errReviewLeaseIdentity):
		return "lease_identity_changed"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "deadline_exceeded"
	case ctx.Err() != nil:
		return "cancelled"
	default:
		return ""
	}
}

func describeReviewProcess(ctx context.Context, child *exec.Cmd, err error, started time.Time) reviewProcessReport {
	d := reviewProcessReport{Version: 1, Category: "completed", DurationMS: time.Since(started).Milliseconds()}
	if child.ProcessState != nil {
		code := child.ProcessState.ExitCode()
		d.ExitCode = &code
		d.Signal = reviewProcessSignal(child.ProcessState)
	}
	switch {
	case reviewCancellationCategory(ctx) != "":
		d.Category = reviewCancellationCategory(ctx)
	case child.Process == nil:
		d.Category = "provider_start_failed"
	case d.Signal != 0:
		d.Category = "provider_signal"
	case err != nil:
		d.Category = "provider_exit"
	}
	return d
}

func readReviewProcessReport(path string) (reviewProcessReport, bool) {
	var d reviewProcessReport
	if !readReviewDiagnosticJSON(path, &d) || d.Version != 1 || d.DurationMS < 0 || d.DurationMS > (2*time.Hour).Milliseconds() || d.Signal < 0 || d.Signal > 127 || (d.ExitCode != nil && (*d.ExitCode < -1 || *d.ExitCode > 255)) {
		return reviewProcessReport{}, false
	}
	switch d.Category {
	case "completed", "provider_start_failed", "provider_exit", "provider_signal", "cancelled", "deadline_exceeded":
		return d, true
	default:
		return reviewProcessReport{}, false
	}
}

func readReviewDiagnosticJSON(path string, out any) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

// Status must never render free-form text from a retained diagnostic, including
// diagnostics written by older versions that retained provider failure text.
func readReviewExecutionDiagnostic(path string) (reviewExecutionDiagnostic, bool) {
	var d reviewExecutionDiagnostic
	if !readReviewDiagnosticJSON(path, &d) || d.Version != 1 || (d.Provider != "claude" && d.Provider != "codex") {
		return reviewExecutionDiagnostic{}, false
	}
	attempt, err := hex.DecodeString(strings.ReplaceAll(d.AttemptID, "-", ""))
	if err != nil || len(attempt) != 16 || len(d.AttemptID) != 36 || d.AttemptID[8] != '-' || d.AttemptID[13] != '-' || d.AttemptID[18] != '-' || d.AttemptID[23] != '-' {
		return reviewExecutionDiagnostic{}, false
	}
	if d.Signal < 0 || d.Signal > 127 || (d.ExitCode != nil && (*d.ExitCode < -1 || *d.ExitCode > 255)) || (d.APIErrorStatus != 0 && (d.APIErrorStatus < 400 || d.APIErrorStatus > 599)) || d.RecordedAt <= 0 || d.StdoutBytes < 0 || d.StdoutBytes > 8<<20 || d.StderrBytes < 0 || d.StderrBytes > 64<<10 {
		return reviewExecutionDiagnostic{}, false
	}
	for _, duration := range []int64{d.DurationMS, d.StageDurationMS, d.ProviderDurationMS} {
		if duration < 0 || duration > (2*time.Hour).Milliseconds() {
			return reviewExecutionDiagnostic{}, false
		}
	}
	switch d.Stage {
	case "brief", "checkout", "findings", "provider_setup", "child_guard", "provider", "result":
	default:
		return reviewExecutionDiagnostic{}, false
	}
	switch d.Category {
	case "provider_start_failed", "provider_exit", "provider_signal", "cancelled", "deadline_exceeded",
		"lease_expired", "lease_rejected", "lease_identity_changed", "execution_failed", "review_api_error",
		"review_api_unavailable", "child_guard_failed", "provider_error", "invalid_request", "authentication_required",
		"access_denied", "rate_limited", "provider_unavailable", "invalid_result", "provider_reported_failure":
		return d, true
	default:
		return reviewExecutionDiagnostic{}, false
	}
}

func (d reviewExecutionDiagnostic) failure() error {
	detail := ""
	if d.ExitCode != nil {
		detail += fmt.Sprintf(", exit %d", *d.ExitCode)
	}
	if d.Signal != 0 {
		detail += fmt.Sprintf(", signal %d", d.Signal)
	}
	if d.APIErrorStatus != 0 {
		detail += fmt.Sprintf(", HTTP %d", d.APIErrorStatus)
	}
	return fmt.Errorf("review failed at %s (%s%s); see private last-provider-diagnostic.json", d.Stage, d.Category, detail)
}
