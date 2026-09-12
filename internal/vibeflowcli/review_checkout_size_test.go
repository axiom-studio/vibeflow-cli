package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in replay of the real user's exact checkout, without a provider call,
// model invocation, source mutation, or test-only checkout implementation.
func TestReviewCheckoutRepositoryAcceptance(t *testing.T) {
	source := os.Getenv("PR_REVIEW_CHECKOUT_SOURCE")
	if source == "" {
		t.Skip("set PR_REVIEW_CHECKOUT_SOURCE, PR_REVIEW_CHECKOUT_BASE and PR_REVIEW_CHECKOUT_HEAD")
	}
	base, head := os.Getenv("PR_REVIEW_CHECKOUT_BASE"), os.Getenv("PR_REVIEW_CHECKOUT_HEAD")
	if !reviewSHA(base) || !reviewSHA(head) {
		t.Fatal("exact base and head SHAs are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	remote := reviewTestGit(t, source, "remote", "get-url", "origin")
	host, repository, err := reviewRemoteIdentity(remote)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PR_REVIEW_CHECKOUT_THREE_TREES") == "1" {
		// Advance only a disposable target branch to exercise three full source
		// snapshots. The original repository remains read-only.
		original := source
		source = t.TempDir()
		reviewTestGit(t, source, "clone", "--shared", "--no-checkout", original, ".")
		reviewTestGit(t, source, "remote", "set-url", "origin", remote)
		tree := reviewTestGit(t, source, "rev-parse", base+"^{tree}")
		base = reviewTestGit(t, source, "commit-tree", tree, "-p", base, "-m", "Private target advancement for checkout acceptance")
		reviewTestGit(t, source, "update-ref", "refs/heads/review-fixture-target", base)
		reviewTestGit(t, source, "symbolic-ref", "HEAD", "refs/heads/review-fixture-target")
	}
	before := reviewTestGit(t, source, "status", "--porcelain")
	beforeHead := reviewTestGit(t, source, "rev-parse", "HEAD")
	e := &reviewExecution{}
	e.Review.ProviderHost = host
	e.Attempt.Round.BaseSHA, e.Attempt.Round.HeadSHA = base, head
	e.Attempt.Round.Details = reviewRepository{BaseRepositoryName: repository, HeadRepositoryName: repository, BaseCloneURL: remote, HeadCloneURL: remote}
	w := &reviewWatch{root: t.TempDir()}
	p := &reviewReceipt{RequestID: reviewUUID()}
	root := w.workDir(p)
	defer func() {
		if err := w.cleanup(p); err != nil {
			t.Errorf("owned checkout cleanup: %v", err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Errorf("owned checkout remains: %v", err)
		}
	}()
	started := time.Now()
	if err := prepareReviewCheckout(ctx, source, root, e); err != nil {
		t.Fatalf("exact repository preparation after %s: %v", time.Since(started), err)
	}
	t.Logf("prepared full repository in %s", time.Since(started))
	mergeBase := reviewTestGit(t, source, "merge-base", base, head)
	trees := map[string]string{"base": base, "head": head}
	if mergeBase != base {
		trees["merge-base"] = mergeBase
	}
	for name, sha := range trees {
		entries, err := reviewGit(ctx, source, "ls-tree", "-rlz", "--full-tree", sha)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		var total int64
		for _, entry := range bytes.Split(bytes.TrimSuffix(entries, []byte{0}), []byte{0}) {
			parts := bytes.SplitN(entry, []byte{'\t'}, 2)
			meta := strings.Fields(string(parts[0]))
			if len(parts) != 2 || len(meta) != 4 {
				t.Fatalf("unexpected tree entry: %q", entry)
			}
			if meta[1] != "blob" {
				continue
			}
			n, err := strconv.ParseInt(meta[3], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(filepath.Join(root, "input", name, string(parts[1])))
			if err != nil {
				t.Fatalf("missing tracked file %s/%s: %v", name, parts[1], err)
			}
			var digest hash.Hash = sha1.New()
			if len(meta[2]) == 64 {
				digest = sha256.New()
			}
			fmt.Fprintf(digest, "blob %d\x00", n)
			copied, copyErr := io.Copy(digest, f)
			f.Close()
			if copyErr != nil || copied != n || fmt.Sprintf("%x", digest.Sum(nil)) != meta[2] {
				t.Fatalf("tracked content changed %s/%s: %v", name, parts[1], copyErr)
			}
			count++
			total += n
		}
		t.Logf("verified %s: %d tracked blobs, %d bytes (all blob hashes match)", name, count, total)
	}
	var diskBytes int64
	if err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			diskBytes += info.Size()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("prepared directory including Git objects: %d logical bytes; verification elapsed %s", diskBytes, time.Since(started))
	if reviewTestGit(t, source, "status", "--porcelain") != before || reviewTestGit(t, source, "rev-parse", "HEAD") != beforeHead {
		t.Fatal("source checkout was modified")
	}
}

func TestReviewCheckoutLargeTrackedDependenciesRemainBounded(t *testing.T) {
	repo := t.TempDir()
	reviewTestGit(t, repo, "init", "--bare")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	gitInput := func(input []byte, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	blob := gitInput(make([]byte, 16<<20), "hash-object", "-w", "--stdin")
	small := gitInput([]byte("x"), "hash-object", "-w", "--stdin")
	var entries strings.Builder
	for i := 0; i < 32; i++ {
		fmt.Fprintf(&entries, "100644 blob %s\tdependency%02d.bin\n", blob, i)
	}
	for _, extra := range []bool{false, true} {
		t.Run(fmt.Sprintf("extra_byte_%t", extra), func(t *testing.T) {
			input := entries.String()
			if extra {
				input += fmt.Sprintf("100644 blob %s\tlast.bin\n", small)
			}
			vendor := gitInput([]byte(input), "mktree")
			tree := gitInput([]byte(fmt.Sprintf("040000 tree %s\tvendor\n", vendor)), "mktree")
			dest := filepath.Join(t.TempDir(), "export")
			err := exportReviewTree(ctx, repo, tree, dest)
			if extra {
				if err == nil || !strings.Contains(err.Error(), "bounded input size") {
					t.Fatalf("tree larger than 512 MiB was not rejected: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("512 MiB tracked dependency tree rejected: %v", err)
			}
			files, err := os.ReadDir(filepath.Join(dest, "vendor"))
			if err != nil || len(files) != 32 {
				t.Fatalf("tracked dependencies omitted: %d %v", len(files), err)
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil || info.Size() != 16<<20 {
					t.Fatalf("tracked dependency truncated: %s %v", file.Name(), err)
				}
			}
		})
	}
}
