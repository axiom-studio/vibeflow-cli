// Command fakeserver is a local stand-in for the VibeFlow review API that
// offers `vibeflow review-watch --once` exactly one review job: the fixture
// repository's HEAD against its parent. It writes the submitted result (or
// failure) to -result and keeps serving until it is killed.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The repository link and project review-watch is started with.
const linkID = 7

func uuid() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func rev(repo, name string) string {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", name+"^{commit}").Output()
	if err != nil {
		log.Fatalf("git rev-parse %s in %s: %v", name, repo, err)
	}
	return strings.TrimSpace(string(out))
}

func main() {
	repo := flag.String("repo", "", "fixture repository (from build-fixture.sh)")
	promptPath := flag.String("prompt", "", "review prompt the server sends Vera")
	resultPath := flag.String("result", "", "where to write the submitted result or failure")
	addrFile := flag.String("addr-file", "", "where to write the listening host:port")
	budget := flag.Duration("deadline", 60*time.Minute, "review round deadline")
	flag.Parse()
	if *repo == "" || *promptPath == "" || *resultPath == "" || *addrFile == "" {
		log.Fatal("usage: fakeserver -repo DIR -prompt FILE -result FILE -addr-file FILE")
	}
	prompt, err := os.ReadFile(*promptPath)
	if err != nil {
		log.Fatal(err)
	}
	head, base := rev(*repo, "HEAD"), rev(*repo, "HEAD^")

	// The brief carries project context in production; the fixture has none.
	brief := json.RawMessage(`{}`)
	digest := sha256.Sum256(brief)
	jobID, roundID, attemptID := uuid(), uuid(), uuid()
	cloneURL := "https://github.com/acme/review-fixture.git"
	review := map[string]any{"id": jobID, "provider": "github", "provider_host": "github.com", "repository_link_id": linkID, "number": 197, "head_sha": head, "base_sha": base}

	deadline := time.Now().Add(*budget)
	var mu sync.Mutex
	runnerID, claimed := "", false
	execution := func() map[string]any {
		return map[string]any{
			"version": 2, "progress_reporting_version": 1, "review": review, "prompt": string(prompt),
			"pull_request": map[string]any{
				"title":       "Webhook secrets, Starter plan gate, legacy check names, retention purge and diff anchors",
				"description": "Adds webhook signing secrets to linked repositories and keeps them out of the API, a Starter plan workflow gate, the legacy status check name for older repositories, a retention purge for old review sessions, a diff parser for comment anchors, and the settings page's secret field.",
				"author":      "fixture-author",
				"discussion":  []any{},
			},
			"attempt": map[string]any{
				"id": attemptID, "runner_id": runnerID, "session_id": "", "lease_expires_at": time.Now().Add(5 * time.Minute).UnixMilli(),
				"round": map[string]any{
					"id": roundID, "job_id": jobID, "head_sha": head, "base_sha": base, "deadline_at": deadline.UnixMilli(),
					"details": map[string]any{"head_repository_name": "acme/review-fixture", "base_repository_name": "acme/review-fixture", "head_clone_url": cloneURL, "base_clone_url": cloneURL},
				},
			},
		}
	}
	save := func(body []byte) {
		if err := os.WriteFile(*resultPath, body, 0600); err != nil {
			log.Print(err)
		}
	}
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		switch {
		case r.Method == "GET" && strings.HasSuffix(p, "/pr-review-sessions"):
			reply(w, map[string]any{"sessions": []any{}})
		case r.Method == "GET" && strings.HasSuffix(p, "/pr-review-repositories"):
			reply(w, map[string]any{"repositories": []any{}, "supported_runner_capabilities": []string{"repository_review_v1", "finding_category_v1"}})
		case r.Method == "POST" && strings.HasSuffix(p, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			runnerID, _ = body["id"].(string)
			body["user_id"] = 1
			reply(w, body)
		case strings.HasSuffix(p, "/work"):
			reviews := []any{}
			if !claimed {
				reviews = append(reviews, review)
			}
			reply(w, map[string]any{"reviews": reviews})
		case strings.HasSuffix(p, "/claim"):
			claimed = true
			reply(w, execution())
		case strings.HasSuffix(p, "/renew"): // Extends the lease; the round deadline stays.
			reply(w, execution())
		case strings.HasSuffix(p, "/brief"):
			reply(w, map[string]any{"round_id": roundID, "digest": hex.EncodeToString(digest[:]), "content": brief})
		case strings.HasSuffix(p, "/result"), strings.HasSuffix(p, "/fail"):
			var body json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", 400)
				return
			}
			if strings.HasSuffix(p, "/fail") {
				var fail struct {
					Reason string `json:"reason"`
				}
				json.Unmarshal(body, &fail)
				body, _ = json.Marshal(map[string]any{"result": nil, "failure_reason": fail.Reason})
			}
			save(body)
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/heartbeat"), r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			log.Printf("fakeserver: unexpected %s %s", r.Method, p)
			http.NotFound(w, r)
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*addrFile, []byte(ln.Addr().String()), 0600); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "fakeserver: review %s at %.12s (base %.12s) on http://%s\n", jobID, head, base, ln.Addr())
	log.Fatal(http.Serve(ln, handler))
}
