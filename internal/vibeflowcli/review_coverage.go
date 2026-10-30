package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// reviewCoverageCapability is advertised by a server that accepts a result's
// coverage and the "incomplete" outcome; an older one rejects both.
const reviewCoverageCapability = "coverage_v1"

// reviewCoverageMaxFiles is the server's coverage bound (GitHub's own
// changed-file listing limit).
const reviewCoverageMaxFiles = 3000

// reviewManifestFile is one changed file of the review, merge base to head:
// a rename is its new path, a deletion keeps its path with status D. Skip
// names the class the runner detected that Vera may leave unread.
type reviewManifestFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Skip   string `json:"skip,omitempty"`
}

type reviewManifest struct {
	Files []reviewManifestFile `json:"files"`
	// Unnameable counts changed files whose path the server cannot name in
	// coverage (see reviewCoveragePath); any of them makes a review partial,
	// so an odd file name cannot hide a change from the contract.
	Unnameable int    `json:"unnameable,omitempty"`
	Digest     string `json:"-"`
}

var reviewLockfiles = map[string]bool{"package-lock.json": true, "yarn.lock": true, "go.sum": true, "pnpm-lock.yaml": true, "Cargo.lock": true, "poetry.lock": true}

// reviewBase answers what the merge base already holds. The pull request
// author controls every head path and byte, so the classes that trust content
// (generated, vendored) are decided against the base, and any error reading
// it means the file is reviewed.
type reviewBase struct {
	ctx           context.Context
	objects, sha  string
	vendoredByDir map[string]bool
}

func (b *reviewBase) tree(dir string) bool {
	kind, err := reviewGit(b.ctx, b.objects, "cat-file", "-t", b.sha+":"+dir)
	return err == nil && strings.TrimSpace(string(kind)) == "tree"
}

func (b *reviewBase) blob(file string) ([]byte, bool) {
	data, err := reviewGit(b.ctx, b.objects, "cat-file", "blob", b.sha+":"+file)
	return data, err == nil
}

// vendored reports whether a file's vendored package already existed at the
// base: for Go vendor/ with a modules.txt, a module that file lists; otherwise
// the vendored directory's first-level entry (or the directory itself, for a
// file directly in it). A dependency the pull request introduces is reviewed.
func (b *reviewBase) vendored(file string) bool {
	dirs := strings.Split(path.Dir(file), "/")
	at := slices.IndexFunc(dirs, func(d string) bool { return d == "vendor" || d == "node_modules" || d == "third_party" })
	if at < 0 {
		return false
	}
	key := path.Dir(file)
	if trusted, ok := b.vendoredByDir[key]; ok {
		return trusted
	}
	root, rest := strings.Join(dirs[:at+1], "/"), dirs[at+1:]
	trusted := false
	if modules, ok := b.blob(root + "/modules.txt"); ok && dirs[at] == "vendor" && len(rest) > 0 {
		pkg := strings.Join(rest, "/")
		for _, line := range strings.Split(string(modules), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "#" && (pkg == f[1] || strings.HasPrefix(pkg, f[1]+"/")) {
				trusted = true
				break
			}
		}
	} else if len(rest) > 0 {
		trusted = b.tree(root + "/" + rest[0])
	} else {
		trusted = b.tree(root)
	}
	b.vendoredByDir[key] = trusted
	return trusted
}

// reviewGeneratedMarker is the first "Code generated ... DO NOT EDIT" line in
// the first five lines of content, or nil.
func reviewGeneratedMarker(content []byte) []byte {
	lines := bytes.SplitN(content[:min(len(content), 1024)], []byte("\n"), 6)
	for _, line := range lines[:min(5, len(lines))] {
		if bytes.Contains(line, []byte("Code generated")) && bytes.Contains(line, []byte("DO NOT EDIT")) {
			return line
		}
	}
	return nil
}

// reviewSkipClass is the deterministic skippable class of a changed file, or
// "": deleted, binary (numstat "-"), vendored (its package existed at the
// base), lockfile, or generated (a file modified in place that already had a
// generated name, or the same "Code generated ... DO NOT EDIT" header line,
// at the base). A new, renamed or copied file is never generated, and a
// header the pull request adds or edits does not make a file skippable.
func reviewSkipClass(base *reviewBase, head, file, status string, binary bool) string {
	switch {
	case status == "D":
		return "deleted"
	case binary:
		return "binary"
	case base.vendored(file):
		return "vendored"
	}
	name := path.Base(file)
	if reviewLockfiles[name] {
		return "lockfile"
	}
	if status != "M" {
		return ""
	}
	if strings.HasSuffix(name, ".pb.go") || strings.Contains(name, "_generated.") {
		return "generated"
	}
	// Only a regular file is read: a link the pull request adds could point
	// outside the checkout.
	local := filepath.Join(head, filepath.FromSlash(file))
	if info, err := os.Lstat(local); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(local)
	if err != nil {
		return ""
	}
	defer f.Close()
	start := make([]byte, 1024)
	n, _ := io.ReadFull(f, start)
	marker := reviewGeneratedMarker(start[:n])
	if marker == nil {
		return ""
	}
	if old, ok := base.blob(file); ok && bytes.Equal(reviewGeneratedMarker(old), marker) {
		return "generated"
	}
	return ""
}

// reviewCoveragePath mirrors the server's finding path rule: a path it would
// refuse cannot be named in coverage, so it is left out of the manifest.
func reviewCoveragePath(p string) bool {
	return p != "" && len(p) <= 512 && !path.IsAbs(p) && path.Clean(p) == p && p != "." && p != ".." && !strings.HasPrefix(p, "../") && !strings.HasPrefix(p, `"`) && !strings.ContainsAny(p, "\\\x00\r\n\t") && !(len(p) >= 2 && p[1] == ':')
}

// buildReviewManifest lists the changed files from mergeBase to headSHA in
// the private object store, classifying each against the head worktree and
// the merge base.
func buildReviewManifest(ctx context.Context, objects, head, mergeBase, headSHA string) (*reviewManifest, error) {
	names, err := reviewGit(ctx, objects, "diff", "--no-ext-diff", "--no-textconv", "-M", "--name-status", "-z", mergeBase, headSHA, "--")
	if err != nil {
		return nil, err
	}
	numstat, err := reviewGit(ctx, objects, "diff", "--no-ext-diff", "--no-textconv", "-M", "--numstat", "-z", mergeBase, headSHA, "--")
	if err != nil {
		return nil, err
	}
	binary := map[string]bool{}
	// Each record is "added\tdeleted\tpath\0", or "added\tdeleted\t\0old\0new\0" for a rename.
	fields := strings.Split(string(numstat), "\x00")
	for i := 0; i < len(fields); i++ {
		counts := strings.SplitN(fields[i], "\t", 3)
		if len(counts) != 3 {
			continue
		}
		file := counts[2]
		if file == "" && i+2 < len(fields) {
			file = fields[i+2]
			i += 2
		}
		binary[file] = counts[0] == "-" && counts[1] == "-"
	}
	manifest := &reviewManifest{Files: []reviewManifestFile{}}
	base := &reviewBase{ctx: ctx, objects: objects, sha: mergeBase, vendoredByDir: map[string]bool{}}
	// Each record is "S\0path\0", or "Rnn\0old\0new\0" for a rename or copy.
	parts := strings.Split(string(names), "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		status, file := parts[i], parts[i+1]
		if status == "" {
			break
		}
		if status[0] == 'R' || status[0] == 'C' {
			if i+2 >= len(parts) {
				break
			}
			file = parts[i+2]
			i++
		}
		status = status[:1]
		if !reviewCoveragePath(file) {
			manifest.Unnameable++
			continue
		}
		manifest.Files = append(manifest.Files, reviewManifestFile{Path: file, Status: status, Skip: reviewSkipClass(base, head, file, status, binary[file])})
	}
	encoded, _ := json.Marshal(manifest)
	sum := sha256.Sum256(encoded)
	manifest.Digest = hex.EncodeToString(sum[:])
	return manifest, nil
}

// readReviewManifest reads the manifest written beside the checkout.
func readReviewManifest(path string) (*reviewManifest, error) {
	data, err := reviewReadBounded(path, 4<<20)
	if err != nil {
		return nil, err
	}
	var m reviewManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(bytes.TrimSpace(data))
	m.Digest = hex.EncodeToString(sum[:])
	return &m, nil
}

type reviewCoverageFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// applyReviewCoverage replaces the harness's coverage with one entry per
// manifest file: reviewed when Vera said so, skipped only for a class the
// runner detected, unreviewed otherwise. A clean outcome with any unreviewed
// file becomes "incomplete"; nothing is ever upgraded. It returns the
// unreviewed paths.
func applyReviewCoverage(result json.RawMessage, m *reviewManifest) (json.RawMessage, []string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(result, &top); err != nil {
		return nil, nil, fmt.Errorf("review provider returned invalid JSON")
	}
	var claimed []reviewCoverageFile
	_ = json.Unmarshal(top["coverage"], &claimed) // Missing or malformed coverage reviews nothing.
	reviewed := map[string]bool{}
	for _, c := range claimed {
		if c.Status == "reviewed" {
			reviewed[c.Path] = true
		}
	}
	coverage := make([]reviewCoverageFile, 0, len(m.Files))
	var missing []string
	for _, f := range m.Files {
		switch {
		case reviewed[f.Path]:
			coverage = append(coverage, reviewCoverageFile{Path: f.Path, Status: "reviewed"})
		case f.Skip != "":
			coverage = append(coverage, reviewCoverageFile{Path: f.Path, Status: "skipped", Reason: f.Skip})
		default:
			coverage = append(coverage, reviewCoverageFile{Path: f.Path, Status: "unreviewed"})
			missing = append(missing, f.Path)
		}
	}
	if len(coverage) > reviewCoverageMaxFiles {
		// ponytail: past the server's bound the rest cannot be accounted for,
		// so the review is partial; split such pull requests to review them.
		for _, f := range coverage[reviewCoverageMaxFiles:] {
			if f.Status != "skipped" {
				missing = append(missing, f.Path)
			}
		}
		coverage = coverage[:reviewCoverageMaxFiles]
	}
	if m.Unnameable > 0 {
		missing = append(missing, fmt.Sprintf("%d file(s) with a path coverage cannot name", m.Unnameable))
	}
	var outcome string
	_ = json.Unmarshal(top["outcome"], &outcome)
	if outcome == "clean" && len(missing) > 0 {
		top["outcome"], _ = json.Marshal("incomplete")
	}
	top["coverage"], _ = json.Marshal(coverage)
	top["manifest_digest"], _ = json.Marshal(m.Digest)
	out, err := json.Marshal(top)
	return out, missing, err
}

// stripReviewCoverage removes coverage for a server without coverage_v1.
func stripReviewCoverage(result json.RawMessage) json.RawMessage {
	var top map[string]json.RawMessage
	if json.Unmarshal(result, &top) != nil {
		return result
	}
	if _, ok := top["coverage"]; !ok {
		return result
	}
	delete(top, "coverage")
	delete(top, "manifest_digest")
	out, _ := json.Marshal(top)
	return out
}

// reviewCoverageTask is the task line that holds Vera to the manifest.
const reviewCoverageTask = "- ../manifest.json lists every changed file (merge base to head; a rename is its new path, a deletion has status D). Review every file whose \"skip\" is empty, and list each manifest file once in coverage: status reviewed only for a file you actually read and reviewed, or skipped with reason set to its \"skip\" value (generated, vendored, lockfile, binary or deleted); a file you could not review in time is left out. Never skip a file whose \"skip\" is empty. The runner checks coverage against the manifest: any file not reviewed makes a clean outcome a partial review that cannot pass, so when time is short review the riskiest files first and say in Limits which you did not reach.\n"

// checkReviewCoverage holds the result to the manifest before it is
// submitted, or strips coverage for a server without coverage_v1.
func (w *reviewWatch) checkReviewCoverage(result json.RawMessage, manifestPath string) (json.RawMessage, error) {
	if !w.coverage {
		return stripReviewCoverage(result), nil
	}
	manifest, err := readReviewManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("review manifest is unreadable")
	}
	result, missing, err := applyReviewCoverage(result, manifest)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		shown := missing[:min(len(missing), 10)]
		more := ""
		if len(missing) > len(shown) {
			more = fmt.Sprintf(" and %d more", len(missing)-len(shown))
		}
		fmt.Fprintf(w.output, "Vera did not review %d of %d changed files (%s%s); a clean verdict is published as a partial review.\n", len(missing), len(manifest.Files), strings.Join(shown, ", "), more)
	}
	return result, nil
}
