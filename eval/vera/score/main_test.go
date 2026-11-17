package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAnswers() answers {
	return answers{
		Defects: []answer{
			{ID: "leak", Required: true, Locations: []location{{Path: "repos/list.go", StartLine: 10, EndLine: 12}}},
			{ID: "race", Locations: []location{{Path: "a.go", StartLine: 20, EndLine: 30}, {Path: "b.go", StartLine: 5, EndLine: 5}}},
		},
		Decoys: []answer{{ID: "hmac", Locations: []location{{Path: "verify.go", StartLine: 1, EndLine: 9}}}},
	}
}

func TestScoreMatchesByPathAndTolerantLine(t *testing.T) {
	r := score(testAnswers(), []finding{
		{Path: "./repos/list.go", Line: 13, Severity: "blocker"}, // within tolerance
		{Path: "repos/list.go", Line: 11},                        // duplicate: a hit, not a second catch
		{Path: "b.go", Line: 8, Severity: "advisory"},            // second location
		{Path: "verify.go", Line: 4, Severity: "blocker"},        // decoy
		{Path: "a.go", Line: 40},                                 // outside every range
	}, 3)
	if len(r.Caught) != 2 || r.Hits != 3 || len(r.Missed) != 0 || len(r.DecoyHits) != 1 || len(r.Unmatched) != 1 || r.Findings != 5 {
		t.Fatalf("report %+v", r)
	}
	if r.Caught["leak"].Line != 13 {
		t.Fatalf("first match not kept: %+v", r.Caught["leak"])
	}
	if got := r.failures(bar{MinCaught: 2}); len(got) != 1 || !strings.Contains(got[0], "decoy") {
		t.Fatalf("failures %v", got)
	}
	if got := r.failures(bar{MinCaught: 2, MaxDecoyBlockers: 1}); len(got) != 0 {
		t.Fatalf("failures %v", got)
	}
	if got := r.failures(bar{MinCaught: 2, MaxDecoyBlockers: 1, MinPrecision: 0.8}); len(got) != 1 || !strings.Contains(got[0], "precision 0.60") {
		t.Fatalf("failures %v", got)
	}
}

func TestScoreToleranceZeroAndWrongPath(t *testing.T) {
	r := score(testAnswers(), []finding{{Path: "repos/list.go", Line: 13}, {Path: "other/list.go", Line: 11}}, 0)
	if len(r.Caught) != 0 || len(r.Missed) != 2 || len(r.Unmatched) != 2 {
		t.Fatalf("report %+v", r)
	}
	got := r.failures(bar{MinCaught: 1})
	if len(got) != 2 || !strings.Contains(got[1], "missed required defect leak") {
		t.Fatalf("failures %v", got)
	}
}

func TestReadFindingsAcceptsEnvelopeAndBareResult(t *testing.T) {
	for _, doc := range []string{
		`{"result":{"outcome":"changes_requested","new_findings":[{"path":"a.go","line":2,"title":"t","severity":"blocker"}]},"failure_reason":null}`,
		`{"outcome":"changes_requested","new_findings":[{"path":"a.go","line":2,"title":"t","severity":"blocker"}]}`,
	} {
		f, err := readFindings([]byte(doc))
		if err != nil || len(f) != 1 || f[0].Path != "a.go" || f[0].Line != 2 || f[0].Severity != "blocker" {
			t.Fatalf("%s: %+v %v", doc, f, err)
		}
	}
	if f, err := readFindings([]byte(`{"outcome":"clean","new_findings":[]}`)); err != nil || len(f) != 0 {
		t.Fatalf("clean: %+v %v", f, err)
	}
	if _, err := readFindings([]byte(`{"result":null,"failure_reason":"checkout failed"}`)); err == nil || !strings.Contains(err.Error(), "checkout failed") {
		t.Fatalf("failure: %v", err)
	}
	if _, err := readFindings([]byte(`{"outcome":"clean"}`)); err == nil {
		t.Fatal("a result without new_findings was accepted")
	}
}

func TestReadAnswersRejectsBadEntries(t *testing.T) {
	for _, doc := range []string{
		`{"defects":[{"id":"x","locations":[{"path":"a.go","start_line":1,"end_line":1}]},{"id":"x","locations":[{"path":"a.go","start_line":1,"end_line":1}]}]}`,
		`{"defects":[{"id":"x","locations":[]}]}`,
		`{"decoys":[{"id":"x","locations":[{"path":"a.go","start_line":5,"end_line":4}]}]}`,
	} {
		if _, err := readAnswers([]byte(doc)); err == nil {
			t.Fatalf("accepted %s", doc)
		}
	}
}

// The shipped answers.json scores a review that finds every defect as a
// pass, and one that misses the required sibling-code leak as a failure.
func TestRunAgainstShippedAnswers(t *testing.T) {
	data, err := os.ReadFile("../answers.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := readAnswers(data)
	if err != nil {
		t.Fatal(err)
	}
	write := func(extra []finding, skip ...string) string {
		list := append([]finding{}, extra...)
	next:
		for _, d := range a.Defects {
			for _, s := range skip {
				if d.ID == s {
					continue next
				}
			}
			l := d.Locations[0]
			list = append(list, finding{Key: d.ID, Title: d.ID, Severity: "blocker", Path: l.Path, Line: l.StartLine})
		}
		data, _ := json.Marshal(map[string]any{"result": map[string]any{"base_sha": "b1", "head_sha": "h1", "new_findings": list}, "failure_reason": nil})
		p := filepath.Join(t.TempDir(), "result.json")
		os.WriteFile(p, data, 0600)
		return p
	}
	var out bytes.Buffer
	if code := run([]string{"-answers", "../answers.json", "-result", write(nil)}, &out); code != 0 || !strings.Contains(out.String(), "Recall 8/8") || !strings.Contains(out.String(), "PASS") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	// The report names the reviewed identity, the incomplete fix, what was
	// checked and the limits.
	for _, want := range []string{
		"Reviewed base b1, head h1",
		"h-tag-projection-stale-snapshot [incomplete fix: the base already had it]:",
		"Checks: 8 new finding(s) matched by path and line (tolerance 3) against 8 seeded defects and 4 decoys.",
		"First review only: the fake server sends no earlier rounds or findings, so incremental-round coverage is not measured.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := run([]string{"-answers", "../answers.json", "-result", write(nil, a.Defects[0].ID)}, &out); code != 1 || !strings.Contains(out.String(), "missed required defect "+a.Defects[0].ID) {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	// One non-required miss meets the bar of 7; two do not.
	out.Reset()
	if code := run([]string{"-answers", "../answers.json", "-result", write(nil, "h-tag-projection-stale-snapshot")}, &out); code != 0 ||
		!strings.Contains(out.String(), "h-tag-projection-stale-snapshot [incomplete fix: the base already had it] at tags/store.go:42-44") {
		t.Fatalf("one miss: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"-answers", "../answers.json", "-result", write(nil, "h-tag-projection-stale-snapshot", "g-diff-parser-trusts-header-lines-in-hunks")}, &out); code != 1 || !strings.Contains(out.String(), "caught 6 of 8 defects, the bar is 7") {
		t.Fatalf("two misses: exit %d\n%s", code, out.String())
	}
	// Reporting the row-locking label projection as the same race is a
	// blocker on a decoy, which fails the bar.
	out.Reset()
	lockDecoy := finding{Title: "stale labels_json under concurrent writers", Severity: "blocker", Path: "notes/labels.go", Line: 62}
	if code := run([]string{"-answers", "../answers.json", "-result", write([]finding{lockDecoy})}, &out); code != 1 || !strings.Contains(out.String(), "on decoy decoy-label-projection-locks-note-row") {
		t.Fatalf("decoy: exit %d\n%s", code, out.String())
	}
}
