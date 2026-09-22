//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewSupervisorReconcileLifetime(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\nprintf '%s\\n' '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var registrations, stops atomic.Int64
	var blockHeartbeat atomic.Bool
	blocked := make(chan struct{}, 1)
	releaseHeartbeat := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["user_id"] = 42
			registrations.Add(1)
			_ = json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			if blockHeartbeat.Load() {
				select {
				case blocked <- struct{}{}:
				default:
				}
				select {
				case <-r.Context().Done():
				case <-releaseHeartbeat:
				}
				return
			}
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/work"):
			fmt.Fprint(w, `{"reviews":[]}`)
		case r.Method == "DELETE":
			stops.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer func() { close(releaseHeartbeat); server.Close() }()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "fixture"
	cfg.Providers["claude"] = Provider{Binary: provider}
	o := reviewStartupOptions(cfg, "", repo)
	o.ProjectID, o.RepositoryLinkID, o.GitProvider, o.Repository = 66, 7, "github", repo
	external, err := startReviewOwned(context.Background(), cfg, "", o)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o.RepositoryLinkID = 8
	d := reviewDiscovery{Complete: true, Projects: []Project{{ID: 66, Name: "A"}}, Bindings: []reviewBinding{{Options: o, ProjectName: "A"}}}
	externalOptions := o
	externalOptions.RepositoryLinkID = 7
	d.Bindings = append(d.Bindings, reviewBinding{Options: externalOptions})
	statuses := s.Reconcile(d)
	if len(statuses) != 2 || statuses[0].State != "online" || statuses[1].State != "external" {
		t.Fatalf("statuses %+v", statuses)
	}
	for range 2 {
		s.Reconcile(d)
	}
	if registrations.Load() != 2 {
		t.Fatalf("refresh duplicated runners: %d", registrations.Load())
	}
	// A transient project failure keeps the owned runner. A definitive revocation stops it.
	s.Reconcile(reviewDiscovery{Projects: d.Projects, Complete: true, Problems: map[int64]string{66: "offline"}})
	if stops.Load() != 0 {
		t.Fatal("transient failure stopped runner")
	}
	s.Reconcile(reviewDiscovery{Projects: d.Projects, Complete: true, Problems: map[int64]string{66: "denied"}, Revoked: map[int64]bool{66: true}})
	if stops.Load() != 1 {
		t.Fatal("revocation did not stop owned runner")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-external.Done():
		t.Fatal("supervisor stopped external runner")
	default:
	}
	// Close must cancel a startup before attempting to acquire its reconcile lock.
	s2, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	blockHeartbeat.Store(true)
	o.RepositoryLinkID = 9
	d.Bindings = []reviewBinding{{Options: o}}
	reconciled := make(chan struct{})
	go func() { s2.Reconcile(d); close(reconciled) }()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("startup never reached heartbeat")
	}
	closed := make(chan error, 1)
	go func() { closed <- s2.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on the startup mutex before cancellation")
	}
	<-reconciled
}

func TestReviewSupervisorOwnsImmutableSnapshots(t *testing.T) {
	withTempRoot(t)
	cfg := DefaultConfig()
	cfg.APIToken = "snapshot-token"
	cfg.DirectoryHistory = []string{"/initial"}
	cfg.Providers["claude"] = Provider{Binary: "/bin/sh", Env: map[string]string{"ANTHROPIC_API_KEY": "initial"}}
	cfg.SavedEnvVars = map[string]string{"key": "initial"}
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 1000 {
			cfg.DirectoryHistory[0] = "changed"
			cfg.Worktree.LastCustomDir = "changed"
			cfg.Providers["claude"].Env["ANTHROPIC_API_KEY"] = "changed"
			cfg.SavedEnvVars["key"] = "changed"
		}
	}()
	for range 1000 {
		if s.cfg.DirectoryHistory[0] != "/initial" || s.cfg.Providers["claude"].Env["ANTHROPIC_API_KEY"] != "initial" || s.cfg.SavedEnvVars["key"] != "initial" {
			t.Fatal("supervisor shares live configuration")
		}
	}
	wg.Wait()
	s.statuses = []reviewRunnerStatus{{State: "needs_checkout", Binding: reviewBinding{Checkouts: []reviewStartupCheckout{{Path: "/initial", Links: []reviewStartupChoice{{Value: "github:7"}}}}}}}
	copy := s.Snapshot()
	copy[0].Binding.Checkouts[0].Path = "changed"
	copy[0].Binding.Checkouts[0].Links[0].Value = "changed"
	got := s.Snapshot()
	if got[0].Binding.Checkouts[0].Path != "/initial" || got[0].Binding.Checkouts[0].Links[0].Value != "github:7" {
		t.Fatal("returned status aliases supervisor state")
	}
}
