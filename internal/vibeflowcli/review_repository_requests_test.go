package vibeflowcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewRepositoryRequestNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, advertisement, provider    string
		capability, discovery, questions bool
	}{
		{"supported", `"repository_review_v1","question_v1"`, "github", true, true, true},
		{"reviews_only", `"repository_review_v1"`, "github", true, true, false},
		// A server routing claim kinds already routes question claims.
		{"claim_kinds", `"repository_review_v1","claim_kind_v1"`, "github", true, true, true},
		// A runner that reports coverage says so; only such a runner claims reviews.
		{"coverage", `"repository_review_v1","question_v1","coverage_v1"`, "github", true, true, true},
		{"bitbucket_coverage", `"coverage_v1"`, "bitbucket", false, true, false},
		{"old_server", "", "github", false, true, false},
		{"unknown_capability", `"future_review_v2"`, "github", false, true, false},
		// Bitbucket still reads discovery, to check the checkout against its link.
		{"bitbucket_unchanged", `"repository_review_v1","question_v1"`, "bitbucket", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTempRoot(t)
			var registration map[string]any
			discoveries := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/pr-review-repositories"):
					discoveries++
					fmt.Fprintf(w, `{"repositories":[],"supported_runner_capabilities":[%s]}`, tc.advertisement)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
					if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
						t.Error(err)
					}
					response := map[string]any{"id": registration["id"], "user_id": 42, "provider": tc.provider, "repository_link_id": 7}
					_ = json.NewEncoder(w).Encode(response)
				case strings.HasSuffix(r.URL.Path, "/heartbeat"), r.Method == "DELETE":
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/work"):
					fmt.Fprint(w, `{"reviews":[]}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken = server.URL, "fixture"
			// Hermetic preflight: CI has no installed claude.
			provider := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(provider, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg.Providers["claude"] = Provider{Binary: provider}
			var output bytes.Buffer
			var statuses []string
			watch := reviewWatch{onStatus: func(status string) { statuses = append(statuses, status) }, client: NewClient(server.URL, "fixture"), cfg: cfg, output: &output, options: reviewWatchOptions{
				ProjectID: 23, RepositoryLinkID: 7, GitProvider: tc.provider, Kind: "local", Name: "negotiation",
				Provider: "claude", Once: true, PollInterval: time.Second,
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := watch.run(ctx); err != nil {
				t.Fatal(err)
			}
			// Repository requests and questions are registered only with the
			// server's consent (issue #5737).
			var caps []string
			if tc.capability {
				caps = append(caps, "repository_review_v1")
			}
			if tc.questions {
				caps = append(caps, "question_v1")
			}
			if strings.Contains(tc.advertisement, `"coverage_v1"`) {
				caps = append(caps, "coverage_v1")
			}
			want := fmt.Sprint(caps)
			if value := fmt.Sprint(registration["capabilities"]); value != want {
				t.Fatalf("capabilities sent=%v want=%v, registration=%v", value, want, registration)
			}
			if disclosed := strings.Contains(output.String(), "anyone who comments"); disclosed != tc.capability {
				t.Fatalf("repository-request scope disclosed=%v with capability=%v", disclosed, tc.capability)
			}
			if disclosed := strings.Contains(output.String(), "Questions enabled: collaborators who comment @vibeflow ask"); disclosed != tc.questions {
				t.Fatalf("question scope disclosed=%v with questions=%v:\n%s", disclosed, tc.questions, output.String())
			}
			if listening := strings.Contains(output.String(), reviewListeningLine); listening != tc.questions {
				t.Fatalf("listening line offers questions=%v with questions=%v", listening, tc.questions)
			}
			if (discoveries == 1) != tc.discovery {
				t.Fatalf("discovery calls=%d want enabled=%v", discoveries, tc.discovery)
			}
			downgraded := tc.provider == "github" && !tc.capability
			if downgraded && !strings.Contains(output.String(), "server upgrade") {
				t.Fatal("legacy downgrade lacks upgrade guidance")
			}
			// Owned and detached runners discard output; the notice must reach their status channel.
			if notified := strings.Contains(strings.Join(statuses, "\n"), "server upgrade"); notified != downgraded {
				t.Fatalf("upgrade status notified=%v want=%v: %q", notified, downgraded, statuses)
			}
		})
	}
}
