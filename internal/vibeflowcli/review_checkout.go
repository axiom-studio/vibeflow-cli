package vibeflowcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const reviewGitObjectBudget int64 = 2 << 30

func reviewSHA(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && (len(s) == 40 || len(s) == 64) && strings.ToLower(s) == s
}

func reviewRemoteURL(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(raw, ":")
		if !ok {
			return nil, fmt.Errorf("invalid repository remote")
		}
		raw = "ssh://" + host + "/" + path
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || strings.ContainsAny(raw, "%\\\x00\r\n\t ") || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return nil, fmt.Errorf("repository remote must be HTTPS or SSH without embedded credentials")
	}
	if u.User != nil {
		user := u.User.Username()
		tenant, enterprise := strings.CutSuffix(strings.ToLower(u.Hostname()), ".ghe.com")
		_, password := u.User.Password()
		validTenant := tenant != "" && len(tenant) <= 63 && strings.Trim(tenant, "abcdefghijklmnopqrstuvwxyz0123456789-") == "" && !strings.HasPrefix(tenant, "-") && !strings.HasSuffix(tenant, "-")
		if u.Scheme != "ssh" || password || (user != "git" && !(enterprise && validTenant && user == tenant)) {
			return nil, fmt.Errorf("repository SSH username must be git or the GHE.com tenant, without a password")
		}
	}
	name := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	parts := strings.Split(name, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(name, "%\\\x00\r\n\t ?#") || parts[0] == ".." || parts[1] == ".." {
		return nil, fmt.Errorf("invalid repository identity")
	}
	return u, nil
}

func reviewRemoteIdentity(raw string) (string, string, error) {
	u, err := reviewRemoteURL(raw)
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	return strings.ToLower(u.Hostname()), strings.ToLower(name), nil
}

func reviewGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-c", "protocol.ext.allow=never", "-C", dir}, args...)
	cmd := exec.Command("git", argv...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
	var output limitedReviewBuffer
	output.limit = 16 << 20
	cmd.Stdout = &output
	if err := runReviewProcess(ctx, cmd); err != nil {
		return nil, fmt.Errorf("review git %s failed", args[0])
	}
	return output.Bytes(), nil
}

func fetchReviewObjects(ctx context.Context, objects, remote, sha string, budget int64) error {
	check := func() error {
		var total int64
		return filepath.WalkDir(objects, func(path string, entry os.DirEntry, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if os.IsNotExist(err) { // Git atomically renames incoming packs.
				return nil
			}
			if err != nil || entry.IsDir() {
				return err
			}
			info, err := entry.Info()
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			total += info.Size()
			if total > budget {
				return fmt.Errorf("review Git objects exceed the %d-byte acquisition budget", budget)
			}
			return nil
		})
	}
	if err := check(); err != nil {
		return err
	}
	fetchCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		// ponytail: sampled disk budget can overshoot between checks; use a
		// filesystem/container quota when a hard disk ceiling is required.
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-fetchCtx.Done():
				return
			case <-ticker.C:
				if err := check(); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	// Keep incoming objects packed so the budget scan stays small; prevent
	// maintenance and submodule work from adding unrelated acquisition.
	_, err := reviewGit(fetchCtx, objects, "-c", "protocol.file.allow=always", "-c", "fetch.unpackLimit=0", "-c", "gc.auto=0", "fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", remote, sha)
	close(stop)
	<-done
	if cause := context.Cause(fetchCtx); cause != nil {
		return cause
	}
	if err != nil {
		return err
	}
	return check()
}

// Object export never executes repository hooks, filters, submodules or
// attributes. git archive would silently omit export-ignore paths.
func exportReviewTree(ctx context.Context, objects, sha, dest string) error {
	list, err := reviewGit(ctx, objects, "ls-tree", "-rz", "--full-tree", sha)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dest, 0700); err != nil {
		return err
	}
	entries := bytes.Split(bytes.TrimSuffix(list, []byte{0}), []byte{0})
	if len(entries) > 20000 {
		return fmt.Errorf("review tree exceeds 20000 files")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", objects, "cat-file", "--batch")
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { in.Close(); cmd.Process.Kill(); cmd.Wait() }()
	reader := bufio.NewReader(out)
	var total int64
	var special []string
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		parts := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid git tree record")
		}
		fields := strings.Fields(string(parts[0]))
		name := string(parts[1])
		if len(fields) != 3 || !filepath.IsLocal(name) || strings.ContainsAny(name, "\\\x00\r\n") {
			return fmt.Errorf("unsafe git tree path")
		}
		if fields[1] == "commit" {
			special = append(special, "Submodule "+name+" at "+fields[2]+" (not downloaded)")
			continue
		}
		if fields[1] != "blob" {
			return fmt.Errorf("unsupported git object")
		}
		if _, err = io.WriteString(in, fields[2]+"\n"); err != nil {
			return err
		}
		header, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		meta := strings.Fields(header)
		if len(meta) != 3 || meta[0] != fields[2] || meta[1] != "blob" {
			return fmt.Errorf("unexpected git object")
		}
		n, err := strconv.ParseInt(meta[2], 10, 64)
		// Preserve checked-in dependencies in full. At most three snapshots are
		// exported per attempt, each bounded to 512 MiB and streamed to disk.
		if err != nil || n < 0 || n > 16<<20 || total+n > 512<<20 {
			return fmt.Errorf("review tree exceeds bounded input size")
		}
		total += n
		path := filepath.Join(dest, name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.CopyN(file, reader, n)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if b, err := reader.ReadByte(); err != nil || b != '\n' {
			return fmt.Errorf("incomplete git object")
		}
		if fields[0] == "120000" {
			special = append(special, "Symlink "+name+" is materialized as its literal target, not followed")
		}
	}
	if len(special) > 0 {
		return os.WriteFile(dest+"-object-notes.txt", []byte(strings.Join(special, "\n")), 0600)
	}
	return nil
}

func prepareReviewCheckout(ctx context.Context, source, root string, execution *reviewExecution) error {
	round := execution.Attempt.Round
	if !reviewSHA(round.BaseSHA) || !reviewSHA(round.HeadSHA) {
		return fmt.Errorf("review requires exact commit SHAs")
	}
	remote, err := reviewGit(ctx, source, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	host, name, err := reviewRemoteIdentity(strings.TrimSpace(string(remote)))
	if err != nil || host != execution.Review.ProviderHost || name != strings.ToLower(round.Details.BaseRepositoryName) {
		return fmt.Errorf("selected checkout does not match the review repository")
	}
	origin, _ := reviewRemoteURL(strings.TrimSpace(string(remote)))
	objects := filepath.Join(root, "objects.git")
	if err = os.MkdirAll(objects, 0700); err != nil {
		return err
	}
	if _, err = reviewGit(ctx, objects, "init", "--bare"); err != nil {
		return err
	}
	for _, item := range []struct{ sha, remote, name string }{{round.BaseSHA, round.Details.BaseCloneURL, round.Details.BaseRepositoryName}, {round.HeadSHA, round.Details.HeadCloneURL, round.Details.HeadRepositoryName}} {
		fetch := source
		if _, err := reviewGit(ctx, source, "cat-file", "-e", item.sha+"^{commit}"); err != nil {
			fetch = item.remote
			h, n, err := reviewRemoteIdentity(fetch)
			if err != nil || h != host || n != strings.ToLower(item.name) {
				return fmt.Errorf("review clone identity is invalid")
			}
			// Keep the operator's Git authentication transport, including for forks
			// on the same verified provider host. Never copy backend credentials.
			if origin.Scheme == "ssh" {
				clone, _ := reviewRemoteURL(fetch)
				clone.Scheme, clone.User = origin.Scheme, origin.User
				fetch = clone.String()
			}
		}
		if err = fetchReviewObjects(ctx, objects, fetch, item.sha, reviewGitObjectBudget); err != nil {
			return err
		}
		if _, err = reviewGit(ctx, objects, "cat-file", "-e", item.sha+"^{commit}"); err != nil {
			return err
		}
	}
	bases, err := reviewGit(ctx, objects, "merge-base", "--all", round.BaseSHA, round.HeadSHA)
	if err != nil {
		return fmt.Errorf("cannot establish PR merge base; fetch complete base and head history in the selected checkout")
	}
	ancestors := strings.Fields(string(bases))
	if len(ancestors) != 1 || !reviewSHA(ancestors[0]) {
		return fmt.Errorf("PR has no unique merge base; resolve divergent history before reviewing")
	}
	mergeBase := ancestors[0]
	input := filepath.Join(root, "input")
	if err = exportReviewTree(ctx, objects, round.BaseSHA, filepath.Join(input, "base")); err != nil {
		return err
	}
	if err = exportReviewTree(ctx, objects, round.HeadSHA, filepath.Join(input, "head")); err != nil {
		return err
	}
	baselineDirectory := "base"
	if mergeBase != round.BaseSHA {
		baselineDirectory = "merge-base"
		if err = exportReviewTree(ctx, objects, mergeBase, filepath.Join(input, baselineDirectory)); err != nil {
			return err
		}
	}
	if err = saveReviewJSON(filepath.Join(input, "revisions.json"), map[string]string{"target_base_sha": round.BaseSHA, "head_sha": round.HeadSHA, "merge_base_sha": mergeBase, "merge_base_directory": baselineDirectory, "diff": "merge_base_sha..head_sha; base/ is target integration context"}); err != nil {
		return err
	}
	diff, err := reviewGit(ctx, objects, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", mergeBase, round.HeadSHA, "--")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(input, "review.diff"), diff, 0600)
}

type limitedReviewBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedReviewBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("review output exceeds size limit")
	}
	return b.Buffer.Write(p)
}
