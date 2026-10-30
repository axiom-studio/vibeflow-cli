//go:build darwin || linux

package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewPassLimitsChooseOneOrManyPasses(t *testing.T) {
	cfg := DefaultConfig()
	l := reviewPassLimitsFor(cfg)
	if l != (reviewPassLimits{15, 60 << 10, 3}) {
		t.Fatalf("defaults = %+v", l)
	}
	files := func(n int) []string { return make([]string, n) }
	for _, c := range []struct {
		files, diff int
		want        bool
	}{{15, 60 << 10, false}, {16, 1, true}, {2, 60<<10 + 1, true}, {1, 1 << 20, false}, {0, 0, false}} {
		if got := l.multiPass(files(c.files), c.diff); got != c.want {
			t.Errorf("multiPass(%d files, %d bytes) = %v", c.files, c.diff, got)
		}
	}
	cfg.ReviewSinglePassMaxFiles, cfg.ReviewSinglePassMaxDiffKB, cfg.ReviewSpecialistParallelism = 2, 1, 5
	if l := reviewPassLimitsFor(cfg); l != (reviewPassLimits{2, 1 << 10, 5}) {
		t.Fatalf("configured = %+v", l)
	}
}

// Every reviewable file lands in exactly one specialist pass: the plan's
// first area wins, a file the plan missed goes to general, a name that is
// not a reviewable file is dropped, and a large area is split.
func TestReviewSpecialistGroupsCoverEveryFileOnce(t *testing.T) {
	reviewable := []string{"auth.go", "db.sql", "ui.tsx", "a.go", "b.go", "c.go"}
	plan := &reviewPlan{Areas: map[string][]string{
		"security": {"auth.go", "not-in-the-pr.go"},
		"data":     {"db.sql", "auth.go"},
		"frontend": {"ui.tsx"},
		"general":  {"a.go"},
	}}
	got := map[string][]string{}
	for _, p := range reviewSpecialistGroups(plan, reviewable, 2) {
		got[p.Name] = p.Files
		if p.Area == "" || reviewAreaFocus(p.Area) == "" {
			t.Errorf("pass %s has no area focus", p.Name)
		}
	}
	want := map[string][]string{"security": {"auth.go"}, "data": {"db.sql"}, "frontend": {"ui.tsx"}, "general": {"a.go", "b.go"}, "general-2": {"c.go"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	// No plan at all: everything is general.
	if passes := reviewSpecialistGroups(nil, reviewable, 15); len(passes) != 1 || passes[0].Name != "general" || len(passes[0].Files) != 6 {
		t.Fatalf("without a plan: %+v", passes[0])
	}
}

func TestMergeReviewCoverageIsTheUnion(t *testing.T) {
	result := json.RawMessage(`{"outcome":"clean","coverage":[{"path":"a.go","status":"reviewed"}]}`)
	merged, err := mergeReviewCoverage(result, []reviewCoverageFile{{Path: "a.go", Status: "reviewed"}, {Path: "b.go", Status: "reviewed"}})
	if err != nil {
		t.Fatal(err)
	}
	_, coverage, _ := coverageOf(t, merged)
	if len(coverage) != 2 || coverage[0].Path != "a.go" || coverage[1].Path != "b.go" {
		t.Fatalf("coverage = %+v", coverage)
	}
}

func TestReviewDiffFileLinesCountsEachFile(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n+more\ndiff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n"
	if got := reviewDiffFileLines([]byte(diff)); !reflect.DeepEqual(got, map[string]int{"x.go": 3, "gone.go": 1}) {
		t.Fatalf("lines = %v", got)
	}
}

// passHarness is a fake harness that runs every pass the way the task file
// asks, logging each pass it sees in marks.
const passHarness = `#!/bin/sh
task=$(cat)
marks=MARKS
out=$(printf '%s\n' "$task" | sed -n 's/.*to the absolute path \([^ ]*\) (never.*/\1/p' | head -n 1)
case "$task" in
*"Local review plan task"*) pass=plan ;;
*"Local review task ("*) pass=$(printf '%s\n' "$task" | sed -n 's/^Local review task (\(.*\) pass):$/\1/p') ;;
*"This is the final pass of a multi-pass review"*) pass=integration ;;
*) pass=single ;;
esac
echo "$pass" >> "$marks/passes"
case "$pass" in
plan)
	cp ../plan-input.json "$marks/plan-input.json"
	# main.go is security, hidden.txt data; the plan forgets the rest.
	printf '{"result":{"areas":{"security":["main.go"],"data":["hidden.txt"],"compat":[],"frontend":[],"general":[]}},"failure_reason":null}' > "$out" ;;
single|integration)
	[ "$pass" = integration ] && cp ../candidates.json "$marks/candidates.json"
	cat FINAL > "$out" ;;
*)
	if [ "$pass" = SLOW ]; then sleep 60; fi
	touch "$marks/running-$pass"
	sleep 1
	ls "$marks" | grep -c '^running-' > "$marks/concurrent-$pass"
	files=$(printf '%s\n' "$task" | sed -n '/^- This is one specialist pass/,/^- Focus on/p' | sed -n 's/^  - //p')
	cov=""
	for f in $files; do cov="$cov{\"path\":\"$f\",\"status\":\"reviewed\",\"reason\":\"\"},"; done
	printf '{"result":{"findings":[{"key":"%s-1","title":"candidate from %s"}],"coverage":[%s],"notes":"checked"},"failure_reason":null}' "$pass" "$pass" "${cov%,}" > "$out"
	rm -f "$marks/running-$pass" ;;
esac
`

// runPassReview runs one review of the test repository (four changed,
// reviewable files) with passHarness on a server that accepts coverage, and
// returns the submitted result, the marks directory and the output.
func runPassReview(t *testing.T, configure func(*Config), slow string, budget time.Duration) (map[string]json.RawMessage, string, string) {
	t.Helper()
	withTempRoot(t)
	t.Setenv("HOME", t.TempDir()) // Never the developer's own harness settings.
	repo, e := reviewTestRepo(t)
	e.Attempt.Round.DeadlineAt = time.Now().Add(budget).UnixMilli()
	var mu sync.Mutex
	var submitted []byte
	server, _, results := reviewWatchServerWith(t, map[int64]*reviewExecution{7: e}, []string{"repository_review_v1", "question_v1", reviewCoverageCapability}, func(body []byte) {
		mu.Lock()
		defer mu.Unlock()
		submitted = body
	})
	digest := sha256.Sum256([]byte("{}"))
	final := filepath.Join(t.TempDir(), "final.json")
	// The final pass lists no coverage of its own: the specialists' counts.
	if err := saveReviewJSON(final, map[string]any{"result": map[string]any{"schema_version": 1, "brief_digest": hex.EncodeToString(digest[:]), "head_sha": e.Review.HeadSHA, "base_sha": e.Review.BaseSHA, "outcome": "clean", "summary": "done", "new_findings": []any{}, "reconciliations": []any{}, "coverage": []any{}}, "failure_reason": nil}); err != nil {
		t.Fatal(err)
	}
	marks := t.TempDir()
	script := strings.NewReplacer("MARKS", shellQuote(marks), "FINAL", shellQuote(final), "SLOW", slow).Replace(passHarness)
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := singleSlotConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "passes"
	cfg.Providers["claude"] = Provider{Binary: provider}
	configure(cfg)
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runReviewWatch(context.Background(), &out, "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "passes", "--once"); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if results.Load() != 1 {
		t.Fatalf("results = %d\n%s", results.Load(), out.String())
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(submitted, &result); err != nil {
		t.Fatal(err)
	}
	return result, marks, out.String()
}

func passesRun(t *testing.T, marks string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(marks, "passes"))
	if err != nil {
		t.Fatal(err)
	}
	passes := strings.Fields(string(data))
	sort.Strings(passes)
	return passes
}

func TestReviewWatchSmallReviewRunsOnePass(t *testing.T) {
	result, marks, _ := runPassReview(t, func(*Config) {}, "none", time.Minute)
	if got := passesRun(t, marks); !reflect.DeepEqual(got, []string{"single"}) {
		t.Fatalf("passes = %v", got)
	}
	// One pass that reviewed nothing is a partial review, as before.
	if string(result["outcome"]) != `"incomplete"` {
		t.Fatalf("outcome = %s", result["outcome"])
	}
}

// A large review runs a plan pass, the specialists at once (up to the
// configured parallelism) and one integration pass that sees every
// candidate; the final coverage is the union, so it stays clean.
func TestReviewWatchLargeReviewRunsPlanSpecialistsAndIntegration(t *testing.T) {
	result, marks, out := runPassReview(t, func(cfg *Config) {
		cfg.ReviewSinglePassMaxFiles = 1 // Four files: large, and one file per specialist.
		cfg.ReviewSpecialistParallelism = 3
	}, "none", time.Minute)
	t.Logf("output:\n%s", out)
	if got := passesRun(t, marks); !reflect.DeepEqual(got, []string{"data", "general", "general-2", "integration", "plan", "security"}) {
		t.Fatalf("passes = %v", got)
	}
	var input struct {
		Files []struct{ Path string } `json:"files"`
	}
	data, _ := os.ReadFile(filepath.Join(marks, "plan-input.json"))
	if json.Unmarshal(data, &input) != nil || len(input.Files) != 4 {
		t.Fatalf("plan input = %s", data)
	}
	most := 0
	for _, pass := range []string{"security", "data", "general", "general-2"} {
		data, _ := os.ReadFile(filepath.Join(marks, "concurrent-"+pass))
		n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		most = max(most, n)
	}
	if most < 2 || most > 3 {
		t.Fatalf("at most %d specialists ran at once, want 2 to 3", most)
	}
	var candidates struct {
		Passes     []reviewPassCandidates `json:"passes"`
		Unreviewed []string               `json:"unreviewed"`
	}
	data, _ = os.ReadFile(filepath.Join(marks, "candidates.json"))
	if err := json.Unmarshal(data, &candidates); err != nil {
		t.Fatalf("candidates: %v %s", err, data)
	}
	files := map[string]string{}
	for _, c := range candidates.Passes {
		if c.State != "completed" || len(c.Findings) != 1 || len(c.Coverage) != len(c.Files) {
			t.Errorf("pass %s: %+v", c.Pass, c)
		}
		for _, f := range c.Files {
			files[f] = c.Area
		}
	}
	// The plan forgot .gitattributes and escape: general reviews them.
	if !reflect.DeepEqual(files, map[string]string{"main.go": "security", "hidden.txt": "data", ".gitattributes": "general", "escape": "general"}) || len(candidates.Unreviewed) != 0 {
		t.Fatalf("files = %v unreviewed = %v", files, candidates.Unreviewed)
	}
	outcome, coverage, _ := coverageOf(t, mustJSON(t, result))
	if outcome != "clean" || len(coverage) != 4 {
		t.Fatalf("outcome %s coverage %+v", outcome, coverage)
	}
	for _, c := range coverage {
		if c.Status != "reviewed" {
			t.Fatalf("coverage %+v", coverage)
		}
	}
	for _, want := range []string{"Running claude headless (plan pass)", "Specialist passes (files each): security (1)", "Running claude headless (integration pass)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q", want)
		}
	}
}

// A specialist that runs out of time is stopped at its pass deadline, the
// integration pass still runs within the attempt's deadline, and the
// specialist's file stays unreviewed: the review is partial, never clean.
func TestReviewWatchSpecialistTimeoutLeavesItsFilesUnreviewed(t *testing.T) {
	const budget = 15 * time.Second
	started := time.Now()
	result, marks, out := runPassReview(t, func(cfg *Config) { cfg.ReviewSinglePassMaxFiles = 1 }, "security", budget)
	t.Logf("output:\n%s", out)
	if elapsed := time.Since(started); elapsed > budget {
		t.Fatalf("review took %s, past its %s deadline", elapsed, budget)
	}
	if got := passesRun(t, marks); !reflect.DeepEqual(got, []string{"data", "general", "general-2", "integration", "plan", "security"}) {
		t.Fatalf("passes = %v", got)
	}
	outcome, coverage, _ := coverageOf(t, mustJSON(t, result))
	if outcome != "incomplete" {
		t.Fatalf("outcome = %s", outcome)
	}
	for _, c := range coverage {
		if (c.Path == "main.go") != (c.Status == "unreviewed") {
			t.Fatalf("coverage %+v", coverage)
		}
	}
	data, _ := os.ReadFile(filepath.Join(marks, "candidates.json"))
	if !strings.Contains(string(data), `"unreviewed":["main.go"]`) || !strings.Contains(out, "Pass security stopped at its time limit") || !strings.Contains(out, "Result: partial review") {
		t.Fatalf("candidates %s", data)
	}
}

// Issue #6593: specialists that wait for a slot (one at a time, as on a
// terminal) each get a share of the window. A first pass that never finishes
// is stopped at its share, so the passes queued behind it still run and only
// its own file stays unreviewed.
func TestReviewWatchQueuedSpecialistsShareTheWindow(t *testing.T) {
	const budget = 20 * time.Second
	result, marks, out := runPassReview(t, func(cfg *Config) {
		cfg.ReviewSinglePassMaxFiles = 1
		cfg.ReviewSpecialistParallelism = 1
	}, "security", budget)
	t.Logf("output:\n%s", out)
	if got := passesRun(t, marks); !reflect.DeepEqual(got, []string{"data", "general", "general-2", "integration", "plan", "security"}) {
		t.Fatalf("passes = %v", got)
	}
	outcome, coverage, _ := coverageOf(t, mustJSON(t, result))
	if outcome != "incomplete" {
		t.Fatalf("outcome = %s", outcome)
	}
	for _, c := range coverage {
		if (c.Path == "main.go") != (c.Status == "unreviewed") {
			t.Fatalf("queued passes lost their time: coverage %+v", coverage)
		}
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Each pass launches like the single pass, with its own task: a specialist
// gets the brief, its focus and only its files, and not the whole-manifest
// coverage rule; the integration pass gets the single task plus its steps.
func TestReviewPassTasks(t *testing.T) {
	_, execution := reviewTestRepo(t)
	t.Setenv("HOME", t.TempDir())
	cfg := DefaultConfig()
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "input"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveReviewJSON(filepath.Join(root, "input", "manifest.json"), coverageManifest()); err != nil {
		t.Fatal(err)
	}
	brief := &reviewBrief{Digest: strings.Repeat("b", 64)}
	task := func(pass *reviewPass) string {
		t.Helper()
		if pass.bounded() {
			pass.Dir = filepath.Join(root, "passes", pass.Name)
			if err := os.MkdirAll(pass.Dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		spec, err := prepareReviewPass(context.Background(), cfg, "claude", "", root, execution, brief, "", "", pass)
		if err != nil {
			t.Fatal(err)
		}
		if pass.bounded() && spec.DeadlineAt != pass.Deadline.UnixMilli() {
			t.Fatalf("%s pass deadline %d, want its own", pass.Name, spec.DeadlineAt)
		}
		data, err := os.ReadFile(filepath.Join(pass.dir(root), "task.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	deadline := time.Now().Add(10 * time.Minute)
	specialist := task(&reviewPass{Name: "security", Area: "security", Files: []string{"auth.go", "token.go"}, Deadline: deadline})
	for _, want := range []string{execution.Prompt, "Local review task (security pass):", "Your files:\n  - auth.go\n  - token.go\n", "Focus on security and authorization", "This pass ends at ", reviewValidation, filepath.Join(root, "passes", "security", "result.json")} {
		if !strings.Contains(specialist, want) {
			t.Errorf("specialist task lacks %q", want)
		}
	}
	if strings.Contains(specialist, reviewCoverageTask) {
		t.Error("specialist task carries the whole-manifest coverage rule")
	}
	plan := task(&reviewPass{Name: reviewPlanPass, Deadline: deadline})
	if !strings.Contains(plan, "../plan-input.json") || !strings.Contains(plan, "  - data: ") {
		t.Errorf("plan task:\n%s", plan)
	}
	final := task(&reviewPass{Name: reviewIntegrationPass})
	if !strings.Contains(final, reviewIntegrationTask) || !strings.Contains(final, reviewCoverageTask) || !strings.Contains(final, filepath.Join(root, "result.json")) {
		t.Errorf("integration task:\n%s", final)
	}
	if single := task(nil); strings.Contains(single, reviewIntegrationTask) {
		t.Error("single task carries the integration steps")
	}
}

// A pass's child guard leaves its cleanup marker in the pass directory;
// it holds the attempt's cleanup like the attempt's own marker.
func TestReviewCleanupSeesPassMarkers(t *testing.T) {
	root := t.TempDir()
	id := reviewUUID()
	pass := filepath.Join(root, "work", id, "passes", "security")
	if err := os.MkdirAll(pass, 0700); err != nil {
		t.Fatal(err)
	}
	w := &reviewWatch{root: root}
	if err := w.providerCleanupPending(nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pass, "provider-cleanup-pending.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.providerCleanupPending(nil); !errors.Is(err, errReviewCleanupUnverified) {
		t.Fatalf("pass marker ignored: %v", err)
	}
	if got := reviewAttemptDir(pass); got != filepath.Join(root, "work", id) {
		t.Fatalf("attempt dir of a pass = %s", got)
	}
}
