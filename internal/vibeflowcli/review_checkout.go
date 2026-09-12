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
)

func reviewSHA(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && (len(s) == 40 || len(s) == 64) && strings.ToLower(s) == s
}

func reviewRemoteIdentity(raw string) (string, string, error) {
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		parts := strings.SplitN(strings.TrimPrefix(raw, "git@"), ":", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid repository remote")
		}
		raw = "ssh://git@" + parts[0] + "/" + parts[1]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || (u.Scheme != "https" && u.Scheme != "ssh") || (u.User != nil && (u.Scheme != "ssh" || u.User.String() != "git")) {
		return "", "", fmt.Errorf("repository remote must be HTTPS or SSH without embedded credentials")
	}
	name := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	parts := strings.Split(name, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(name, "%\\\x00\r\n\t ?#") || parts[0] == ".." || parts[1] == ".." {
		return "", "", fmt.Errorf("invalid repository identity")
	}
	return strings.ToLower(u.Hostname()), strings.ToLower(name), nil
}

func reviewGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-c", "protocol.ext.allow=never", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
	var output limitedReviewBuffer
	output.limit = 16 << 20
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("review git %s failed", args[0])
	}
	return output.Bytes(), nil
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
		}
		if _, err = reviewGit(ctx, objects, "-c", "protocol.file.allow=always", "fetch", "--no-tags", "--no-write-fetch-head", fetch, item.sha); err != nil {
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
