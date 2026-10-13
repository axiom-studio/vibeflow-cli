package vibeflowcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewStoreFiles(t *testing.T, objects string) (packs, loose int) {
	t.Helper()
	filepath.WalkDir(filepath.Join(objects, "objects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(objects, "objects"), path)
		switch {
		case strings.HasPrefix(rel, "pack"+string(filepath.Separator)) && strings.HasSuffix(rel, ".pack"):
			packs++
		case !strings.HasPrefix(rel, "info") && !strings.HasPrefix(rel, "pack"):
			loose++
		}
		return nil
	})
	return
}

// Commits already in the selected checkout are read through Git alternates:
// nothing is copied into the private store, the worktrees are complete, and
// the developer's repository is unchanged.
func TestReviewCheckoutSharesLocalObjects(t *testing.T) {
	source, e := reviewTestRepo(t)
	before := reviewTestGit(t, source, "for-each-ref")
	beforeConfig, _ := os.ReadFile(filepath.Join(source, ".git", "config"))
	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, e); err != nil {
		t.Fatal(err)
	}
	objects := filepath.Join(root, "objects.git")
	alternates, err := os.ReadFile(filepath.Join(objects, "objects", "info", "alternates"))
	if err != nil || !strings.Contains(string(alternates), filepath.Join(".git", "objects")) {
		t.Fatalf("alternates not set to the source objects: %q %v", alternates, err)
	}
	if packs, loose := reviewStoreFiles(t, objects); packs != 0 || loose != 0 {
		t.Fatalf("private store copied objects: %d packs, %d loose", packs, loose)
	}
	data, err := os.ReadFile(filepath.Join(root, "input", "head", "main.go"))
	if err != nil || !strings.Contains(string(data), "return 2") {
		t.Fatalf("head worktree: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "input", "base", "main.go")); err != nil || !strings.Contains(string(data), "return 1") {
		t.Fatalf("base snapshot: %q %v", data, err)
	}
	if diff, err := os.ReadFile(filepath.Join(root, "input", "review.diff")); err != nil || !strings.Contains(string(diff), "+func value() int { return 2 }") {
		t.Fatalf("review diff: %q %v", diff, err)
	}
	if after := reviewTestGit(t, source, "for-each-ref"); after != before {
		t.Fatalf("source refs changed:\n%s\n%s", before, after)
	}
	if afterConfig, _ := os.ReadFile(filepath.Join(source, ".git", "config")); string(afterConfig) != string(beforeConfig) {
		t.Fatal("source config changed")
	}
	if list := reviewTestGit(t, source, "worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Fatalf("worktree registered in the source repository: %s", list)
	}
}

// A shallow checkout cannot supply complete history through alternates, so it
// keeps the copying path; a complete checkout is shared.
func TestReviewCheckoutShallowSourceKeepsCopying(t *testing.T) {
	full, _ := reviewTestRepo(t)
	shallow := filepath.Join(t.TempDir(), "shallow")
	reviewTestGit(t, filepath.Dir(shallow), "clone", "--depth", "1", "file://"+full, shallow)
	if got := reviewTestGit(t, shallow, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatalf("fixture is not shallow: %s", got)
	}
	objects := filepath.Join(t.TempDir(), "objects.git")
	reviewTestGit(t, filepath.Dir(objects), "init", "--bare", objects)
	if shareReviewObjects(context.Background(), shallow, objects) {
		t.Fatal("a shallow source was shared through alternates")
	}
	if _, err := os.Stat(filepath.Join(objects, "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("alternates written for a shallow source: %v", err)
	}
	if !shareReviewObjects(context.Background(), full, objects) {
		t.Fatal("a complete source was not shared")
	}
}
