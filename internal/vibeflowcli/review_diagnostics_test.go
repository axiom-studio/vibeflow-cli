package vibeflowcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewDiagnosticReaderRejectsUntrustedMetadata(t *testing.T) {
	valid := reviewExecutionDiagnostic{Version: 1, AttemptID: "9d223965-782f-4c0e-a0d0-1f9af8258e17", Provider: "claude", Stage: "provider", Category: "provider_exit", RecordedAt: time.Now().UnixMilli()}
	for _, tc := range []struct {
		name   string
		mutate func(*reviewExecutionDiagnostic)
		suffix string
		ok     bool
	}{
		{name: "valid", ok: true},
		{name: "unsafe category", mutate: func(d *reviewExecutionDiagnostic) { d.Category = "secret-token" }},
		{name: "unsafe stage", mutate: func(d *reviewExecutionDiagnostic) { d.Stage = "\x1b[2J" }},
		{name: "unsafe provider", mutate: func(d *reviewExecutionDiagnostic) { d.Provider = "private-path" }},
		{name: "unsafe attempt", mutate: func(d *reviewExecutionDiagnostic) { d.AttemptID = "secret-token" }},
		{name: "invalid signal", mutate: func(d *reviewExecutionDiagnostic) { d.Signal = 999 }},
		{name: "invalid duration", mutate: func(d *reviewExecutionDiagnostic) { d.DurationMS = -1 }},
		{name: "trailing value", suffix: " {}"},
		{name: "oversized whitespace", suffix: strings.Repeat(" ", 4096)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := valid
			if tc.mutate != nil {
				tc.mutate(&d)
			}
			data, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "diagnostic.json")
			if err := os.WriteFile(path, append(data, []byte(tc.suffix)...), 0600); err != nil {
				t.Fatal(err)
			}
			if _, ok := readReviewExecutionDiagnostic(path); ok != tc.ok {
				t.Fatalf("accepted=%v want %v", ok, tc.ok)
			}
		})
	}
}

func TestReviewCancellationDiagnosticsPreserveCause(t *testing.T) {
	for _, tc := range []struct {
		cause    error
		category string
	}{
		{errReviewLeaseExpired, "lease_expired"},
		{errReviewLeaseRejected, "lease_rejected"},
		{errReviewLeaseIdentity, "lease_identity_changed"},
		{context.Canceled, "cancelled"},
	} {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(tc.cause)
		if got := reviewCancellationCategory(ctx); got != tc.category {
			t.Fatalf("got %q want %q", got, tc.category)
		}
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := reviewCancellationCategory(ctx); got != "deadline_exceeded" {
		t.Fatalf("got %q", got)
	}
}
