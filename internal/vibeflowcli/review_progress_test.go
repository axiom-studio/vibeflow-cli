package vibeflowcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewRenewBodyCapability(t *testing.T) {
	_, execution := reviewTestRepo(t)
	data, err := json.Marshal(reviewRenewBody(*execution, true, false))
	if err != nil || string(data) != "{}" {
		t.Fatalf("legacy body: %s %v", data, err)
	}

	execution.ProgressReportingVersion = 1
	data, err = json.Marshal(reviewRenewBody(*execution, true, false))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Progress reviewProgressInput `json:"progress"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body.Progress.HeadSHA != execution.Attempt.Round.HeadSHA ||
		body.Progress.BaseSHA != execution.Attempt.Round.BaseSHA ||
		!body.Progress.CheckoutPrepared || body.Progress.ReviewCompleted {
		t.Fatalf("incorrect progress payload: %+v", body.Progress)
	}
	data, err = json.Marshal(reviewRenewBody(*execution, true, true))
	if err != nil || json.Unmarshal(data, &body) != nil || !body.Progress.ReviewCompleted {
		t.Fatalf("completion payload: %s %v", data, err)
	}
}

func TestReviewProgressOutageKeepsDurableResult(t *testing.T) {
	for _, capability := range []int{1, 0} {
		t.Run(fmt.Sprintf("capability=%d", capability), func(t *testing.T) {
			source, execution := reviewTestRepo(t)
			execution.ProgressReportingVersion = capability
			content := json.RawMessage(`{"findings":[]}`)
			digest := sha256.Sum256(content)
			brief := reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: content}
			result := map[string]any{
				"schema_version": 1, "brief_digest": brief.Digest,
				"head_sha": execution.Review.HeadSHA, "base_sha": execution.Review.BaseSHA,
				"outcome": "clean", "summary": "fixture", "new_findings": []any{}, "reconciliations": []any{},
			}
			response, err := json.Marshal(map[string]any{"structured_output": map[string]any{"result": result, "failure_reason": nil}})
			if err != nil {
				t.Fatal(err)
			}

			root := t.TempDir()
			provider := filepath.Join(root, "provider")
			script := "#!/bin/sh\nfor arg in \"$@\"; do\nif [ \"$arg\" = --help ]; then\necho '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\nexit 0\nfi\ndone\nprintf '%s\\n' " + shellQuote(string(response)) + "\n"
			if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}

			var submissions atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/brief"):
					json.NewEncoder(w).Encode(brief)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/heartbeat"):
					w.WriteHeader(http.StatusServiceUnavailable)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
					submissions.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			cfg := DefaultConfig()
			cfg.Providers["claude"] = Provider{Binary: provider}
			receipt := &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution}
			watch := &reviewWatch{
				client: NewClient(server.URL, "token"), cfg: cfg, root: root, output: io.Discard,
				state:   reviewRunnerState{ID: execution.Attempt.RunnerID, Pending: receipt},
				options: reviewWatchOptions{ProjectID: 1, Repository: source, Provider: "claude", Timeout: 2 * time.Second},
			}
			if err := watch.advance(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if submissions.Load() != 1 || len(receipt.Result) == 0 || receipt.Failure != "" {
				t.Fatalf("validated result was not retained: submissions=%d result=%s failure=%q", submissions.Load(), receipt.Result, receipt.Failure)
			}
		})
	}
}

func TestReviewProgressReportsCheckoutWithoutBlockingCompletion(t *testing.T) {
	source, execution := reviewTestRepo(t)
	execution.ProgressReportingVersion = 1
	content := json.RawMessage(`{"findings":[]}`)
	digest := sha256.Sum256(content)
	brief := reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: content}
	result := map[string]any{
		"schema_version": 1, "brief_digest": brief.Digest,
		"head_sha": execution.Review.HeadSHA, "base_sha": execution.Review.BaseSHA,
		"outcome": "clean", "summary": "fixture", "new_findings": []any{}, "reconciliations": []any{},
	}
	response, err := json.Marshal(map[string]any{"structured_output": map[string]any{"result": result, "failure_reason": nil}})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	release := filepath.Join(root, "release-provider")
	provider := filepath.Join(root, "provider")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\nif [ \"$arg\" = --help ]; then\necho '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\nexit 0\nfi\ndone\nwhile [ ! -f " + shellQuote(release) + " ]; do sleep 0.02; done\nprintf '%s\\n' " + shellQuote(string(response)) + "\n"
	if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	prepared := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/brief"):
			json.NewEncoder(w).Encode(brief)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/renew"):
			var body struct {
				Progress reviewProgressInput `json:"progress"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Progress.CheckoutPrepared {
				select {
				case prepared <- struct{}{}:
				default:
				}
			}
			json.NewEncoder(w).Encode(execution)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := DefaultConfig()
	cfg.Providers["claude"] = Provider{Binary: provider}
	watch := &reviewWatch{
		client: NewClient(server.URL, "token"), cfg: cfg, root: root, output: io.Discard,
		state:   reviewRunnerState{ID: execution.Attempt.RunnerID},
		options: reviewWatchOptions{ProjectID: 1, Repository: source, Provider: "claude", Timeout: time.Minute},
	}
	receipt := &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution}
	done := make(chan error, 1)
	go func() {
		_, err := watch.execute(context.Background(), receipt)
		done <- err
	}()

	select {
	case <-prepared:
	case err := <-done:
		t.Fatalf("execution ended before checkout progress: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("checkout progress was not renewed promptly")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
