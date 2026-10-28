package vibeflowcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReviewCheckoutWritesCoverageManifest(t *testing.T) {
	source, execution := reviewTestRepo(t)
	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, execution, true); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, "input", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest reviewManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		paths = append(paths, file.Path)
		if file.Status != "unreviewed" && file.Status != "skipped" {
			t.Errorf("runner assigned non-initial status to %s: %+v", file.Path, file)
		}
	}
	for _, expected := range []string{"main.go", "hidden.txt", ".gitattributes"} {
		if !slices.Contains(paths, expected) {
			t.Errorf("manifest is missing %q: %v", expected, paths)
		}
	}
	if len(paths) > reviewCoverageMaxFiles {
		t.Fatalf("manifest has %d entries", len(paths))
	}
}

func TestReviewCoverageManifestUsesRenamedPathAndMarksDeletion(t *testing.T) {
	source, execution := reviewTestRepo(t)
	reviewTestGit(t, source, "mv", "hidden.txt", "renamed.txt")
	reviewTestGit(t, source, "rm", "main.go")
	reviewTestGit(t, source, "commit", "-m", "rename and delete")
	head := reviewTestGit(t, source, "rev-parse", "HEAD")
	execution.Review.HeadSHA = head
	execution.Attempt.Round.HeadSHA = head
	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, execution, true); err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, "input", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest reviewManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]reviewCoverageFile, len(manifest.Files))
	for _, file := range manifest.Files {
		got[file.Path] = file
	}
	if _, ok := got["hidden.txt"]; ok {
		t.Fatal("rename manifest retained the old path")
	}
	if got["renamed.txt"].Status != "unreviewed" {
		t.Fatalf("rename destination missing from manifest: %+v", got)
	}
	if got["main.go"].Status != "skipped" || got["main.go"].Reason != "deleted" {
		t.Fatalf("deleted path was not marked skipped/deleted: %+v", got["main.go"])
	}
}

func TestReviewCoverageClassification(t *testing.T) {
	head := t.TempDir()
	if err := os.MkdirAll(filepath.Join(head, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(head, "src", "payload.bin"), []byte{0, 1, 2}, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, reason string
		status       byte
	}{
		{"gone.go", "deleted", 'D'},
		{"vendor/lib.go", "vendored", 'M'},
		{"go.sum", "lockfile", 'M'},
		{"src/payload.bin", "binary", 'M'},
		{"api.generated.go", "generated", 'M'},
	} {
		if got := reviewCoverageSkipReason(tc.path, tc.status, head); got != tc.reason {
			t.Errorf("skip reason for %s = %q, want %q", tc.path, got, tc.reason)
		}
	}
	for _, invalid := range []string{"", ".", "../secret", "a/../secret", "/absolute", `a\\b`, "a\x00b", "C:/secret"} {
		if validReviewCoveragePath(invalid) {
			t.Errorf("accepted unsafe coverage path %q", invalid)
		}
	}
}

func TestNormalizeReviewCoverageMakesGapsIncomplete(t *testing.T) {
	manifest := reviewManifest{Files: []reviewCoverageFile{
		{Path: "a.go", Status: "unreviewed"},
		{Path: "generated.go", Status: "skipped", Reason: "generated"},
		{Path: "c.go", Status: "unreviewed"},
	}}
	digestBytes := sha256.Sum256([]byte("fixture manifest"))
	digest := hex.EncodeToString(digestBytes[:])
	result, err := json.Marshal(map[string]any{
		"schema_version": 1, "brief_digest": "brief", "head_sha": "head", "base_sha": "base", "outcome": "clean",
		"new_findings": []any{}, "reconciliations": []any{}, "manifest_digest": digest,
		"coverage": []reviewCoverageFile{{Path: "a.go", Status: "reviewed"}, {Path: "generated.go", Status: "reviewed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeReviewCoverage(result, manifest, digest)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Outcome        string               `json:"outcome"`
		ManifestDigest string               `json:"manifest_digest"`
		Coverage       []reviewCoverageFile `json:"coverage"`
	}
	if err := json.Unmarshal(normalized, &got); err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "incomplete" || got.ManifestDigest != digest || len(got.Coverage) != 3 {
		t.Fatalf("normalized coverage: %+v", got)
	}
	if got.Coverage[1].Status != "skipped" || got.Coverage[1].Reason != "generated" {
		t.Fatalf("runner skip was not preserved: %+v", got.Coverage[1])
	}
	if got.Coverage[2].Status != "unreviewed" || got.Coverage[2].Reason == "" {
		t.Fatalf("missing reviewer entry was not marked unreviewed: %+v", got.Coverage[2])
	}
}

func TestNormalizeReviewCoverageRejectsUntrustedEntries(t *testing.T) {
	manifest := reviewManifest{Files: []reviewCoverageFile{{Path: "a.go", Status: "unreviewed"}}}
	digestBytes := sha256.Sum256([]byte("fixture manifest"))
	digest := hex.EncodeToString(digestBytes[:])
	for _, tc := range []struct {
		name, suppliedDigest string
		coverage             []reviewCoverageFile
	}{
		{"wrong_digest", "wrong", nil},
		{"outside_path", digest, []reviewCoverageFile{{Path: "outside.go", Status: "reviewed"}}},
		{"duplicate_path", digest, []reviewCoverageFile{{Path: "a.go", Status: "reviewed"}, {Path: "a.go", Status: "reviewed"}}},
		{"invalid_skip_reason", digest, []reviewCoverageFile{{Path: "a.go", Status: "skipped", Reason: "unclear"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := json.Marshal(map[string]any{"outcome": "clean", "manifest_digest": tc.suppliedDigest, "coverage": tc.coverage})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := normalizeReviewCoverage(result, manifest, digest); err == nil {
				t.Fatal("untrusted coverage was accepted")
			}
		})
	}
}

func TestReviewResultSchemaNegotiatesCoverage(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var schema map[string]any
		if err := json.Unmarshal(reviewResultSchema(enabled), &schema); err != nil {
			t.Fatal(err)
		}
		result := schema["properties"].(map[string]any)["result"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
		properties := result["properties"].(map[string]any)
		_, coverage := properties["coverage"]
		_, digest := properties["manifest_digest"]
		if coverage != enabled || digest != enabled {
			t.Fatalf("coverage fields present=%v/%v with negotiated=%v", coverage, digest, enabled)
		}
	}
}
