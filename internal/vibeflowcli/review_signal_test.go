//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestReviewCommandTerminalShutdown(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "vibeflow")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, out)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			repo, execution := reviewTestRepo(t)
			root := t.TempDir()
			pidPath := filepath.Join(root, "provider.pid")
			provider := filepath.Join(root, "owned-provider")
			// A real shell process with an inherited-output descendant, both
			// ignoring TERM, exercises the guard's forced group teardown.
			script := "#!/bin/sh\nfor arg in \"$@\"; do\n if [ \"$arg\" = --help ]; then\n echo '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\n exit 0\n fi\ndone\ntrap '' TERM INT\nsleep 60 &\nprintf '%s %s\\n' \"$$\" \"$!\" > " + shellQuote(pidPath) + "\nwait\n"
			if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var failures, results atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
					var request map[string]any
					json.NewDecoder(r.Body).Decode(&request)
					execution.Attempt.RunnerID = request["id"].(string)
					request["user_id"] = 1
					json.NewEncoder(w).Encode(request)
				case strings.Contains(r.URL.Path, "/work?") || strings.HasSuffix(r.URL.Path, "/work"):
					json.NewEncoder(w).Encode(map[string]any{"reviews": []reviewJob{execution.Review}})
				case strings.HasSuffix(r.URL.Path, "/claim"), strings.HasSuffix(r.URL.Path, "/renew"):
					json.NewEncoder(w).Encode(execution)
				case strings.HasSuffix(r.URL.Path, "/brief"):
					digest := sha256.Sum256([]byte("{}"))
					json.NewEncoder(w).Encode(reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: json.RawMessage(`{}`)})
				case strings.HasSuffix(r.URL.Path, "/result"):
					results.Add(1)
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/fail"):
					failures.Add(1)
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/heartbeat"), r.Method == "DELETE":
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected review API: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken = server.URL, "owned-test-token"
			cfg.Providers["claude"] = Provider{Binary: provider}
			config := filepath.Join(root, "config.yaml")
			if err := SaveConfig(cfg, config); err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(root, "watch.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(binary, "--root", root, "--config", config, "review-watch", "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "signal-test", "--once")
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "USER=review-test"}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			var pids []int
			defer func() {
				if len(pids) > 0 && pids[0] > 0 {
					syscall.Kill(-pids[0], syscall.SIGKILL)
				}
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
			}()
			until := time.Now().Add(8 * time.Second)
			for time.Now().Before(until) {
				data, _ := os.ReadFile(pidPath)
				fields := strings.Fields(string(data))
				if len(fields) == 2 {
					for _, field := range fields {
						pid, _ := strconv.Atoi(field)
						pids = append(pids, pid)
					}
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(pids) != 2 || pids[0] <= 0 || pids[1] <= 0 {
				data, _ := os.ReadFile(log.Name())
				t.Fatalf("owned provider did not start: %s", data)
			}
			locks, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "work", "*", "child.lock"))
			if len(locks) != 1 {
				t.Fatalf("missing child lock: %v", locks)
			}
			syscall.Kill(-cmd.Process.Pid, sig)
			time.Sleep(150 * time.Millisecond)
			if lock, err := lockReviewFile(locks[0]); err == nil {
				lock.Close()
				t.Error("guard released its lock while TERM-ignoring provider still runs")
			}
			// A second terminal interruption must not kill the teardown owner.
			syscall.Kill(-cmd.Process.Pid, sig)
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Error("watcher did not exit after terminal interruption")
			}
			for _, pid := range pids {
				until := time.Now().Add(time.Second)
				for time.Now().Before(until) && syscall.Kill(pid, 0) == nil {
					time.Sleep(10 * time.Millisecond)
				}
				if syscall.Kill(pid, 0) == nil {
					t.Errorf("owned provider/descendant %d survived", pid)
				}
			}
			if failures.Load() != 1 || results.Load() != 0 {
				t.Errorf("cancellation receipts: failures=%d results=%d", failures.Load(), results.Load())
			}
			if _, err := os.Stat(filepath.Dir(locks[0])); !os.IsNotExist(err) {
				t.Errorf("owned input/auth directory remains: %v", err)
			}
		})
	}

	for _, sig := range []syscall.Signal{0, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run("startup_"+sig.String(), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "child.json")
			deadline := time.Now().Add(200 * time.Millisecond)
			if sig != 0 {
				deadline = time.Now().Add(10 * time.Second)
			}
			if err := saveReviewJSON(path, reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", "exit 42"}, DeadlineAt: deadline.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "review-child", path)
			pipe, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pipe.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if sig != 0 {
				// Wait until the guard has reached its blocked startup handshake.
				until := time.Now().Add(time.Second)
				for time.Now().Before(until) {
					if _, err := os.Stat(filepath.Join(root, "child.lock")); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				time.Sleep(50 * time.Millisecond)
				cmd.Process.Signal(sig)
			}
			if err := cmd.Wait(); err == nil || ctx.Err() != nil {
				t.Fatalf("startup handshake ignored deadline: %v / %v", err, ctx.Err())
			}
			if lock, err := lockReviewFile(filepath.Join(root, "child.lock")); err != nil {
				t.Fatal("startup guard lock remained held", err)
			} else {
				lock.Close()
			}
		})
	}
}
