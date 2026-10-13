package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewSlotPick(t *testing.T) {
	o := reviewWatchOptions{RepositoryLinkID: 7, GitProvider: "github"}
	other := reviewJob{ID: "other", RepositoryLinkID: 8, Provider: "github", State: "queued"}
	question := reviewJob{ID: "q", RepositoryLinkID: 7, Provider: "github", State: "clean", PendingQuestions: 1}
	review := reviewJob{ID: "r", RepositoryLinkID: 7, Provider: "github", State: "queued"}
	held := reviewJob{ID: "held", RepositoryLinkID: 7, Provider: "github", State: "reviewing", PendingQuestions: 1}
	for _, tc := range []struct {
		slot string
		jobs []reviewJob
		id   string
		kind string
	}{
		{"", []reviewJob{other, question, review}, "q", ""},          // single shared slot: unchanged first-match rule
		{"question", []reviewJob{review, question}, "q", "question"}, // answer slot skips reviews
		{"question", []reviewJob{review, held}, "", ""},              // a question waits for its PR's review
		{"review", []reviewJob{question, review}, "r", "review"},     // review slot prefers reviews
		{"review", []reviewJob{question}, "q", "question"},           // and borrows a question when idle
		{"review", []reviewJob{other}, "", ""},
	} {
		o.SlotKind = tc.slot
		job, kind, ok := reviewSlotPick(o, tc.jobs)
		if ok != (tc.id != "") || job.ID != tc.id || kind != tc.kind {
			t.Fatalf("slot %q picked %q/%q/%v, want %q/%q", tc.slot, job.ID, kind, ok, tc.id, tc.kind)
		}
	}
}

// slotServer offers one queued review and one pending question on the same
// repository link and records which runner claimed which job with which kind.
func slotServer(t *testing.T, review, question *reviewExecution, capabilities []string) (*httptest.Server, func() map[string]string, func() int) {
	t.Helper()
	var mu sync.Mutex
	names := map[string]string{} // runner ID -> runner name
	claimed := map[string]string{}
	results := 0
	digest := sha256.Sum256([]byte("{}"))
	byJob := map[string]*reviewExecution{review.Review.ID: review, question.Review.ID: question}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		job := ""
		for id := range byJob {
			if strings.Contains(r.URL.Path, "/jobs/"+id+"/") {
				job = id
			}
		}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pr-review-repositories"):
			json.NewEncoder(w).Encode(map[string]any{"repositories": []any{}, "supported_runner_capabilities": capabilities})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			id, _ := body["id"].(string)
			names[id], _ = body["name"].(string)
			body["user_id"] = 42
			json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/work"):
			jobs := []reviewJob{}
			if _, ok := claimed[review.Review.ID]; !ok {
				jobs = append(jobs, review.Review)
			}
			if _, ok := claimed[question.Review.ID]; !ok {
				q := question.Review
				q.State, q.PendingQuestions = "clean", 1
				jobs = append(jobs, q)
			}
			json.NewEncoder(w).Encode(map[string]any{"reviews": jobs})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			runner := ""
			for id, name := range names {
				if strings.Contains(r.URL.Path, "/"+id+"/") {
					runner = name
				}
			}
			want := map[string]string{review.Review.ID: "review", question.Review.ID: "question"}[job]
			if _, taken := claimed[job]; taken || body["kind"] != want {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			claimed[job] = runner + " kind=" + body["kind"]
			e := byJob[job]
			for id, name := range names {
				if name == runner {
					e.Attempt.RunnerID = id
				}
			}
			json.NewEncoder(w).Encode(e)
		case strings.HasSuffix(r.URL.Path, "/renew"):
			json.NewEncoder(w).Encode(byJob[job])
		case strings.HasSuffix(r.URL.Path, "/brief"):
			json.NewEncoder(w).Encode(reviewBrief{RoundID: byJob[job].Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: json.RawMessage(`{}`)})
		case strings.HasSuffix(r.URL.Path, "/findings"):
			json.NewEncoder(w).Encode(map[string]any{"findings": []any{}})
		case strings.HasSuffix(r.URL.Path, "/result"):
			results++
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"), strings.HasSuffix(r.URL.Path, "/fail"), r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected review API: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	snapshot := func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range claimed {
			out[k] = v
		}
		return out
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return results
	}
	return server, snapshot, count
}

func slotExecutions(t *testing.T) (string, *reviewExecution, *reviewExecution) {
	repo, review := reviewTestRepo(t)
	review.Review.State = "queued" // as the server lists a review waiting for a runner
	data, _ := json.Marshal(review)
	question := &reviewExecution{}
	json.Unmarshal(data, question)
	question.Review.ID, question.Attempt.ID, question.Attempt.Round.ID = reviewUUID(), "question-"+reviewUUID(), reviewUUID()
	question.Attempt.Round.JobID = question.Review.ID
	reviewWatchQuestionExecution(question)
	return repo, review, question
}

// One listener with an answer slot and a review slot runs both kinds side by
// side: each slot claims only its own kind, and both results arrive.
func TestReviewSlotsClaimByKind(t *testing.T) {
	withTempRoot(t)
	repo, review, question := slotExecutions(t)
	server, claimed, results := slotServer(t, review, question, []string{"repository_review_v1", reviewClaimKindCapability})
	provider := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nif grep -q 'Local question task' ../../task.txt; then cat " + shellQuote(reviewWatchQuestionResult(t, question)) + " > ../../result.json; else cat " + shellQuote(reviewWatchResult(t, review, 0)) + " > ../../result.json; fi\n"
	if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "slot-token"
	cfg.Providers["claude"] = Provider{Binary: provider}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- runReviewWatch(ctx, &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "mac", "--interval", "1s", "--answer-slots", "1", "--review-slots", "1")
	}()
	for results() < 2 && ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	got := claimed()
	// The review slot takes the review; the question goes to whichever slot is
	// free first (a review slot may borrow it), always claimed as a question.
	if !strings.HasSuffix(got[question.Review.ID], " kind=question") || got[review.Review.ID] != "mac · review 1 kind=review" || results() != 2 {
		t.Fatalf("slot claims = %v, results = %d\n%s", got, results(), out.String())
	}
	for _, want := range []string{"Vera slots: 1 answer, 1 review", "[answer 1] Review runner mac · answer 1 is online", "[review 1] Review runner mac · review 1 is online"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("listener output lacks %q:\n%s", want, out.String())
		}
	}
}

// A server that cannot route by kind is refused instead of mixing work.
func TestReviewSlotsRequireClaimKindServer(t *testing.T) {
	withTempRoot(t)
	repo, review, question := slotExecutions(t)
	server, _, _ := slotServer(t, review, question, []string{"repository_review_v1"})
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "slot-token"
	cfg.Providers["claude"] = Provider{Binary: "/bin/true"}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runReviewWatch(context.Background(), &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "mac", "--answer-slots", "1")
	if err == nil || !strings.Contains(err.Error(), "cannot route work to separate answer and review slots") {
		t.Fatalf("old server accepted slots: %v\n%s", err, out.String())
	}
	if err := runReviewWatch(context.Background(), &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--answer-slots", "1", "--background"); err == nil || !strings.Contains(err.Error(), "foreground listener only") {
		t.Fatalf("background slots accepted: %v", err)
	}
	// A console slot window: invalid kinds are refused, and a valid slot still
	// needs a server that routes by kind.
	if err := runReviewWatch(context.Background(), &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--slot-kind", "everything", "--slot-label", "answer 1"); err == nil || !strings.Contains(err.Error(), "--slot-kind must be question or review") {
		t.Fatalf("invalid slot kind accepted: %v", err)
	}
	if err := runReviewWatch(context.Background(), &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "mac · answer 1", "--slot-kind", "question", "--slot-label", "answer 1"); err == nil || !strings.Contains(err.Error(), "cannot route work to separate answer and review slots") {
		t.Fatalf("slot window started against an old server: %v", err)
	}
}
