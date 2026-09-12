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

func TestReviewSavedResultSurvivesLostResponseWithoutRelaunch(t *testing.T) {
	_, execution := reviewTestRepo(t)
	root := t.TempDir()
	receipt := &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution, Result: json.RawMessage(`{"schema_version":1,"summary":"saved result"}`)}
	work := filepath.Join(root, "work", receipt.RequestID)
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(work, "secret-model-only"), []byte("model-key"), 0600)
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/result") {
			t.Errorf("unexpected API call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		if _, err := os.Stat(work); !os.IsNotExist(err) {
			t.Error("model input remained during publication")
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	watch := &reviewWatch{client: NewClient(server.URL, "supervisor-key"), root: root, state: reviewRunnerState{ID: execution.Attempt.RunnerID, Pending: receipt}, options: reviewWatchOptions{ProjectID: 1}, output: io.Discard}
	if err := watch.save(); err != nil {
		t.Fatal(err)
	}
	if err := watch.advance(context.Background(), false); err == nil {
		t.Fatal("lost response was accepted locally")
	}
	data, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A new supervisor has no model configuration or executable. Only the exact
	// persisted receipt can be replayed; a child launch would fail this test.
	recovered := &reviewWatch{client: watch.client, root: root, options: watch.options, output: io.Discard}
	if err = json.Unmarshal(data, &recovered.state); err != nil {
		t.Fatal(err)
	}
	if recovered.state.Pending == nil {
		t.Fatal("pending result was lost")
	}
	if err = recovered.advance(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != string(receipt.Result) || bodies[0] != bodies[1] {
		t.Fatalf("result changed across replay: %q", bodies)
	}
	if recovered.state.Pending != nil {
		t.Fatal("accepted receipt remained pending")
	}
	data, err = os.ReadFile(filepath.Join(root, "last-receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved reviewReceipt
	if json.Unmarshal(data, &saved) != nil || !saved.Completed || string(saved.Result) != string(receipt.Result) {
		t.Fatal("acknowledged result was lost")
	}
}

func TestReviewCommandRecoversWithoutProviderInstallation(t *testing.T) {
	_, execution := reviewTestRepo(t)
	previousRoot, previousConfig := rootDir, flagConfigPath
	t.Cleanup(func() { rootDir = previousRoot; flagConfigPath = previousConfig })
	SetRootDir(t.TempDir())
	receipt := &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution, Result: json.RawMessage(`{"schema_version":1,"summary":"saved result"}`)}
	var results atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/rest/v1/vibeflow/projects/1/pr-review-runners":
			json.NewEncoder(w).Encode(map[string]any{"id": execution.Attempt.RunnerID, "user_id": 1})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.WriteHeader(204)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
			results.Add(1)
			body, _ := io.ReadAll(r.Body)
			if string(body) != string(receipt.Result) {
				t.Error("saved result changed")
			}
			w.WriteHeader(204)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected call, recovery must not discover or claim: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = server.URL
	cfg.APIToken = "supervisor-key"
	cfg.Providers["claude"] = Provider{Binary: filepath.Join(t.TempDir(), "provider-removed")}
	flagConfigPath = filepath.Join(RootDir(), "config.yaml")
	if err := SaveConfig(cfg, flagConfigPath); err != nil {
		t.Fatal(err)
	}
	identity := fmt.Sprintf("%s\n1\n7\ngithub\nlocal\nrecovery", server.URL)
	digest := sha256.Sum256([]byte(identity))
	private := filepath.Join(RootDir(), "review-runners", hex.EncodeToString(digest[:16]))
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveReviewJSON(filepath.Join(private, "state.json"), reviewRunnerState{ID: execution.Attempt.RunnerID, OwnerID: 1, Pending: receipt}); err != nil {
		t.Fatal(err)
	}
	run := func() error {
		cmd := reviewWatchCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--project", "1", "--repository-link", "7", "--provider", "claude", "--name", "recovery", "--once"})
		return cmd.ExecuteContext(context.Background())
	}
	if err := run(); err != nil {
		t.Fatalf("saved receipt depended on removed provider: %v", err)
	}
	if results.Load() != 1 {
		t.Fatal("saved result was not replayed")
	}
	if err := run(); err == nil {
		t.Fatal("new review work accepted missing provider")
	}
}

func TestReviewExecutionHeartbeatsRunnerBeforeRenewal(t *testing.T) {
	_, execution := reviewTestRepo(t)
	execution.Attempt.LeaseExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	execution.Attempt.Round.DeadlineAt = time.Now().Add(time.Minute).UnixMilli()
	renewed := make(chan struct{})
	var heartbeats atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/brief"):
			select {
			case <-renewed:
				http.Error(w, "stop after ordering assertion", 503)
			case <-r.Context().Done():
			}
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			heartbeats.Add(1)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/renew"):
			if heartbeats.Load() == 0 {
				t.Error("attempt renewal did not refresh runner authority")
			}
			json.NewEncoder(w).Encode(execution)
			close(renewed)
		default:
			t.Errorf("unexpected API call %s", r.URL.Path)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	watch := &reviewWatch{client: NewClient(server.URL, "supervisor-key"), root: t.TempDir(), state: reviewRunnerState{ID: execution.Attempt.RunnerID}, options: reviewWatchOptions{ProjectID: 1, Timeout: 15 * time.Second}}
	receipt := &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := watch.execute(ctx, receipt); err == nil {
		t.Fatal("deliberately interrupted brief fetch succeeded")
	}
	if heartbeats.Load() != 1 {
		t.Fatalf("expected one active runner heartbeat, got %d", heartbeats.Load())
	}
	select {
	case <-renewed:
	default:
		t.Fatal("attempt was not renewed")
	}
}
