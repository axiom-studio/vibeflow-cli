// Command score grades a Vera review of the seeded-defect fixture: it matches
// each new finding in a result.json to the defects and decoys in
// answers.json by path and line, prints recall, precision, misses and false
// positives, and exits 1 when the review is below the shipping bar.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

type location struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Anchor    string `json:"anchor"` // text inside the range, so a fixture edit cannot silently move it
}

type answer struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Required    bool       `json:"required"` // the bar fails when this defect is missed
	Locations   []location `json:"locations"`
}

type answers struct {
	Defects []answer `json:"defects"`
	Decoys  []answer `json:"decoys"` // correct changes; a finding on one is a false positive
}

// finding is the part of a result's new finding the scorer reads.
type finding struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
}

func (f finding) String() string {
	return fmt.Sprintf("%s:%d %q (%s)", f.Path, f.Line, f.Title, f.Severity)
}

type decoyHit struct {
	Decoy   string
	Finding finding
}

type report struct {
	Defects   int
	Caught    map[string]finding // defect ID -> its first matching finding
	Missed    []answer
	Hits      int // findings on a defect, duplicates included
	DecoyHits []decoyHit
	Unmatched []finding
	Findings  int
}

// matches reports whether f lies in one of a's locations, widened by tolerance lines.
func (a answer) matches(f finding, tolerance int) bool {
	p := path.Clean(strings.TrimPrefix(strings.ReplaceAll(f.Path, "\\", "/"), "./"))
	for _, l := range a.Locations {
		if p == l.Path && f.Line >= l.StartLine-tolerance && f.Line <= l.EndLine+tolerance {
			return true
		}
	}
	return false
}

func score(a answers, findings []finding, tolerance int) report {
	r := report{Defects: len(a.Defects), Caught: map[string]finding{}, Findings: len(findings)}
next:
	for _, f := range findings {
		for _, d := range a.Defects {
			if d.matches(f, tolerance) {
				r.Hits++
				if _, ok := r.Caught[d.ID]; !ok {
					r.Caught[d.ID] = f
				}
				continue next
			}
		}
		for _, d := range a.Decoys {
			if d.matches(f, tolerance) {
				r.DecoyHits = append(r.DecoyHits, decoyHit{d.ID, f})
				continue next
			}
		}
		r.Unmatched = append(r.Unmatched, f)
	}
	for _, d := range a.Defects {
		if _, ok := r.Caught[d.ID]; !ok {
			r.Missed = append(r.Missed, d)
		}
	}
	return r
}

type bar struct {
	MinCaught        int
	MaxDecoyBlockers int
	MinPrecision     float64 // 0 disables
}

// failures lists every way r misses the bar; none means it passes.
func (r report) failures(b bar) []string {
	var out []string
	if len(r.Caught) < b.MinCaught {
		out = append(out, fmt.Sprintf("caught %d of %d defects, the bar is %d", len(r.Caught), r.Defects, b.MinCaught))
	}
	for _, d := range r.Missed {
		if d.Required {
			out = append(out, "missed required defect "+d.ID)
		}
	}
	blockers := 0
	for _, h := range r.DecoyHits {
		if h.Finding.Severity == "blocker" {
			blockers++
		}
	}
	if blockers > b.MaxDecoyBlockers {
		out = append(out, fmt.Sprintf("%d blocker(s) on correct decoy changes, at most %d allowed", blockers, b.MaxDecoyBlockers))
	}
	if b.MinPrecision > 0 && r.Findings > 0 && r.precision() < b.MinPrecision {
		out = append(out, fmt.Sprintf("precision %.2f below %.2f", r.precision(), b.MinPrecision))
	}
	return out
}

func (r report) precision() float64 {
	if r.Findings == 0 {
		return 0
	}
	return float64(r.Hits) / float64(r.Findings)
}

func (r report) print(w io.Writer, a answers) {
	precision := "no findings"
	if r.Findings > 0 {
		precision = fmt.Sprintf("%d/%d (%.0f%%)", r.Hits, r.Findings, 100*r.precision())
	}
	fmt.Fprintf(w, "Recall %d/%d (%.0f%%), precision %s\n", len(r.Caught), r.Defects, 100*float64(len(r.Caught))/float64(max(r.Defects, 1)), precision)
	fmt.Fprintln(w, "Caught:")
	for _, d := range a.Defects {
		if f, ok := r.Caught[d.ID]; ok {
			fmt.Fprintf(w, "  %s: %s\n", d.ID, f)
		}
	}
	if len(r.Caught) == 0 {
		fmt.Fprintln(w, "  none")
	}
	fmt.Fprintln(w, "Missed:")
	if len(r.Missed) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, d := range r.Missed {
		l := d.Locations[0]
		fmt.Fprintf(w, "  %s at %s:%d-%d: %s\n", d.ID, l.Path, l.StartLine, l.EndLine, d.Description)
	}
	fmt.Fprintln(w, "False positives:")
	if len(r.DecoyHits)+len(r.Unmatched) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, h := range r.DecoyHits {
		fmt.Fprintf(w, "  on decoy %s: %s\n", h.Decoy, h.Finding)
	}
	for _, f := range r.Unmatched {
		fmt.Fprintf(w, "  %s\n", f)
	}
}

// readFindings reads the new findings from a result.json: the harness's
// {"result":...,"failure_reason":...} envelope or the bare result object the
// runner submits. A failed review is an error.
func readFindings(data []byte) ([]finding, error) {
	var doc struct {
		Result        json.RawMessage  `json:"result"`
		FailureReason *string          `json:"failure_reason"`
		NewFindings   *json.RawMessage `json:"new_findings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.NewFindings == nil && len(doc.Result) > 0 && string(doc.Result) != "null" {
		return readFindings(doc.Result)
	}
	if doc.NewFindings == nil {
		if doc.FailureReason != nil {
			return nil, fmt.Errorf("the review failed: %s", *doc.FailureReason)
		}
		return nil, errors.New("result has no new_findings")
	}
	var findings []finding
	return findings, json.Unmarshal(*doc.NewFindings, &findings)
}

func readAnswers(data []byte) (answers, error) {
	var a answers
	if err := json.Unmarshal(data, &a); err != nil {
		return a, err
	}
	seen := map[string]bool{}
	for _, d := range append(append([]answer{}, a.Defects...), a.Decoys...) {
		if d.ID == "" || seen[d.ID] || len(d.Locations) == 0 {
			return a, fmt.Errorf("answer %q needs a unique id and at least one location", d.ID)
		}
		seen[d.ID] = true
		for _, l := range d.Locations {
			if l.Path == "" || l.StartLine < 1 || l.EndLine < l.StartLine {
				return a, fmt.Errorf("answer %s has an invalid location %+v", d.ID, l)
			}
		}
	}
	return a, nil
}

func run(args []string, out io.Writer) int {
	flags := flag.NewFlagSet("score", flag.ContinueOnError)
	flags.SetOutput(out)
	answersPath := flags.String("answers", "answers.json", "seeded defects and decoys")
	resultPath := flags.String("result", "result.json", "Vera's result: the harness envelope or the submitted result object")
	tolerance := flags.Int("tolerance", 3, "lines a finding may sit outside a defect's range and still match")
	var b bar
	flags.IntVar(&b.MinCaught, "min-caught", 6, "defects that must be caught")
	flags.IntVar(&b.MaxDecoyBlockers, "max-decoy-blockers", 0, "blockers allowed on correct decoy changes")
	flags.Float64Var(&b.MinPrecision, "min-precision", 0, "minimum share of findings that hit a defect (0 disables)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	data, err := os.ReadFile(*answersPath)
	if err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	a, err := readAnswers(data)
	if err != nil {
		fmt.Fprintln(out, "answers:", err)
		return 2
	}
	if data, err = os.ReadFile(*resultPath); err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	findings, err := readFindings(data)
	if err != nil {
		fmt.Fprintln(out, "result:", err)
		return 2
	}
	r := score(a, findings, *tolerance)
	r.print(out, a)
	if failed := r.failures(b); len(failed) > 0 {
		fmt.Fprintln(out, "FAIL: "+strings.Join(failed, "; "))
		return 1
	}
	fmt.Fprintln(out, "PASS")
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
