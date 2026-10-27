//go:build darwin || linux

// Package vera holds the seeded-defect evaluation for Vera; see README.md.
// These tests keep the fixture, answers.json and the runner honest without a
// model: a fake harness stands in for Vera.
package vera

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type location struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Anchor    string `json:"anchor"`
}

type answer struct {
	ID        string     `json:"id"`
	Required  bool       `json:"required"`
	Locations []location `json:"locations"`
}

func loadAnswers(t *testing.T) (defects, decoys []answer) {
	t.Helper()
	var a struct{ Defects, Decoys []answer }
	data, err := os.ReadFile("answers.json")
	if err == nil {
		err = json.Unmarshal(data, &a)
	}
	if err != nil {
		t.Fatal(err)
	}
	return a.Defects, a.Decoys
}

func buildFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fixture")
	if out, err := exec.Command("./build-fixture.sh", dir).CombinedOutput(); err != nil {
		t.Fatalf("build-fixture.sh: %v\n%s", err, out)
	}
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Every answer's range holds its anchor text in the head revision, so an
// edit to the fixture cannot silently move a seeded defect away from its
// answer. The required sibling-code leak stays outside the diff.
func TestFixtureMatchesAnswers(t *testing.T) {
	dir := buildFixture(t)
	if again := buildFixture(t); git(t, again, "rev-parse", "HEAD") != git(t, dir, "rev-parse", "HEAD") {
		t.Fatal("the fixture's head SHA is not reproducible")
	}
	changed := map[string]bool{}
	for _, p := range strings.Fields(git(t, dir, "diff", "--name-only", "HEAD^", "HEAD")) {
		changed[p] = true
	}
	defects, decoys := loadAnswers(t)
	if len(defects) != 7 || len(decoys) < 2 {
		t.Fatalf("%d defects and %d decoys", len(defects), len(decoys))
	}
	for _, a := range append(defects, decoys...) {
		for _, l := range a.Locations {
			data, err := os.ReadFile(filepath.Join(dir, l.Path))
			if err != nil {
				t.Fatalf("%s: %v", a.ID, err)
			}
			lines := strings.Split(string(data), "\n")
			if l.StartLine < 1 || l.EndLine > len(lines) || !strings.Contains(strings.Join(lines[l.StartLine-1:l.EndLine], "\n"), l.Anchor) || l.Anchor == "" {
				t.Errorf("%s: anchor %q is not in %s:%d-%d", a.ID, l.Anchor, l.Path, l.StartLine, l.EndLine)
			}
			if a.Required != !changed[l.Path] {
				t.Errorf("%s: %s changed=%v; only the required sibling-code defect lives in unchanged code", a.ID, l.Path, changed[l.Path])
			}
		}
	}
	vet := exec.Command("go", "vet", "./...")
	vet.Dir, vet.Env = dir, append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local")
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("the fixture does not vet: %v\n%s", err, out)
	}
}

// run.sh drives the real CLI through review-watch --once against the fake
// server, with a fake harness that reports every seeded defect, and the
// scorer passes it. HOME is a temporary directory, so nothing touches the
// caller's ~/.claude or ~/.codex.
func TestRunScoresTheSubmittedResult(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the CLI")
	}
	goEnv := map[string]string{}
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		goEnv[name] = strings.TrimSpace(string(out))
	}
	tmp := t.TempDir()
	home, bin := filepath.Join(tmp, "home"), filepath.Join(tmp, "bin")
	os.MkdirAll(home, 0700)
	os.MkdirAll(bin, 0700)

	defects, _ := loadAnswers(t)
	var findings []map[string]any
	for _, d := range defects {
		l := d.Locations[0]
		findings = append(findings, map[string]any{"key": d.ID, "title": d.ID, "severity": "blocker", "path": l.Path, "line": l.StartLine, "category": "correctness", "trigger": "t", "impact": "i", "evidence": "e", "verification": "v", "suggestion": nil})
	}
	digest := sha256.Sum256([]byte("{}"))
	result, _ := json.Marshal(map[string]any{"result": map[string]any{"schema_version": 1, "brief_digest": hex.EncodeToString(digest[:]), "head_sha": "HEAD_SHA", "base_sha": "BASE_SHA", "outcome": "changes_requested", "summary": "Seeded defects.", "new_findings": findings, "reconciliations": []any{}}, "failure_reason": nil})
	template := filepath.Join(tmp, "result.template.json")
	os.WriteFile(template, result, 0600)
	// The harness runs in input/head; the runner's result path is ../../result.json.
	harness := `#!/bin/sh
sha() { grep -o "\"$1\": *\"[0-9a-f]*\"" ../revisions.json | grep -o '[0-9a-f]\{40\}'; }
sed -e "s/HEAD_SHA/$(sha head_sha)/" -e "s/BASE_SHA/$(sha target_base_sha)/" '` + template + `' > ../../result.json
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(harness), 0700); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(tmp, "work")
	cmd := exec.Command("./run.sh")
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + tmp, "VERA_EVAL_WORK=" + work, "GOTOOLCHAIN=local", "GOFLAGS=", "GOCACHE=" + goEnv["GOCACHE"], "GOMODCACHE=" + goEnv["GOMODCACHE"], "GOPATH=" + goEnv["GOPATH"]}
	out, err := cmd.CombinedOutput()
	t.Logf("run.sh output:\n%s", out)
	if err != nil || !strings.Contains(string(out), "Recall 7/7") || !strings.Contains(string(out), "PASS") {
		log, _ := os.ReadFile(filepath.Join(work, "fakeserver.log"))
		t.Fatalf("run.sh: %v\n%s\nfake server:\n%s", err, out, log)
	}
}
