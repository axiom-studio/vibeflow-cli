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
	return reviewGitEnv(ctx, dir, nil, args...)
}

// reviewGitEnv passes secrets such as the proxy authorization through the
// environment, never argv, so they are not visible in the process list.
func reviewGitEnv(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-c", "protocol.ext.allow=never", "-C", dir}, args...)
	cmd := exec.Command("git", argv...)
	// The private object store has no remote, so LFS could never fetch content.
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_LFS_SKIP_SMUDGE=1"), env...)
	var output limitedReviewBuffer
	output.limit = 16 << 20
	cmd.Stdout = &output
	if err := runReviewProcess(ctx, cmd); err != nil {
		return nil, fmt.Errorf("review git %s failed", args[0])
	}
	return output.Bytes(), nil
}

func fetchReviewObjects(ctx context.Context, objects, remote, sha string, budget int64, env ...string) error {
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
	_, err := reviewGitEnv(fetchCtx, objects, env, "-c", "protocol.file.allow=always", "-c", "fetch.unpackLimit=0", "-c", "gc.auto=0", "fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", remote, sha)
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

// Context snapshots (base, merge-base) never execute repository hooks,
// filters, submodules or attributes. git archive would silently omit
// export-ignore paths.
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
		// Preserve checked-in dependencies in full. At most two snapshots are
		// exported per attempt (base and merge-base), each bounded to 512 MiB and streamed to disk.
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

// reviewGitProxy is the server's attempt-scoped, read-only Git endpoint for
// providers whose credentials stay server-side (Azure Repos).
type reviewGitProxy struct {
	URL   string
	Token string
}

func prepareReviewCheckout(ctx context.Context, source, root string, execution *reviewExecution, proxy ...reviewGitProxy) error {
	round := execution.Attempt.Round
	if !reviewSHA(round.BaseSHA) || !reviewSHA(round.HeadSHA) {
		return fmt.Errorf("review requires exact commit SHAs")
	}
	remote, err := reviewGit(ctx, source, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	if execution.Review.Provider == "azure_devops" {
		return prepareAzureReviewCheckout(ctx, source, root, execution, strings.TrimSpace(string(remote)), proxy)
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
	return finishReviewCheckout(ctx, root, objects, round.BaseSHA, round.HeadSHA)
}

// prepareAzureReviewCheckout uses the selected checkout when it already has
// the exact commits and otherwise fetches them through the server proxy. The
// runner token reaches Git only through GIT_CONFIG_* for that one fetch; no
// Azure credential exists on the runner and nothing is written to Git config.
func prepareAzureReviewCheckout(ctx context.Context, source, root string, execution *reviewExecution, remote string, proxy []reviewGitProxy) error {
	round := execution.Attempt.Round
	host, name, err := reviewAzureRemoteIdentity(remote)
	if err != nil || host != strings.ToLower(execution.Review.ProviderHost) || name != strings.ToLower(round.Details.BaseRepositoryName) ||
		!strings.EqualFold(round.Details.HeadRepositoryName, round.Details.BaseRepositoryName) {
		return fmt.Errorf("selected checkout does not match the review repository")
	}
	objects := filepath.Join(root, "objects.git")
	if err = os.MkdirAll(objects, 0700); err != nil {
		return err
	}
	if _, err = reviewGit(ctx, objects, "init", "--bare"); err != nil {
		return err
	}
	for _, sha := range []string{round.BaseSHA, round.HeadSHA} {
		fetch, env := source, []string(nil)
		if _, err := reviewGit(ctx, source, "cat-file", "-e", sha+"^{commit}"); err != nil {
			if len(proxy) != 1 || !reviewProxyURL(proxy[0].URL) || proxy[0].Token == "" || strings.ContainsAny(proxy[0].Token, "\r\n") {
				return fmt.Errorf("Azure review fetch requires the VibeFlow Git proxy")
			}
			fetch = proxy[0].URL
			env = []string{"GIT_CONFIG_COUNT=2",
				"GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Bearer " + proxy[0].Token,
				"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false"}
		}
		if err = fetchReviewObjects(ctx, objects, fetch, sha, reviewGitObjectBudget, env...); err != nil {
			return err
		}
		if _, err = reviewGit(ctx, objects, "cat-file", "-e", sha+"^{commit}"); err != nil {
			return err
		}
	}
	return finishReviewCheckout(ctx, root, objects, round.BaseSHA, round.HeadSHA)
}

func reviewProxyURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasSuffix(u.Path, "/git") &&
		(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")))
}

// reviewAzureRemoteIdentity maps an Azure Repos remote to its host and
// lowercased project/repository. It accepts HTTPS (.../{project}/_git/{repo},
// the default-project .../_git/{repo} form, and an org@ username without a
// password) and dev.azure.com SSH v3 remotes.
func reviewAzureRemoteIdentity(raw string) (string, string, error) {
	invalid := fmt.Errorf("invalid Azure Repos remote")
	if strings.ContainsAny(raw, "\\\x00\r\n\t") {
		return "", "", invalid
	}
	segment := func(s string) bool { return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\?#") }
	if rest, ok := strings.CutPrefix(raw, "git@ssh.dev.azure.com:v3/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || !segment(parts[0]) {
			return "", "", invalid
		}
		project, err1 := url.PathUnescape(parts[1])
		repo, err2 := url.PathUnescape(strings.TrimSuffix(parts[2], ".git"))
		if err1 != nil || err2 != nil || !segment(project) || !segment(repo) {
			return "", "", invalid
		}
		return "dev.azure.com", strings.ToLower(project + "/" + repo), nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", invalid
	}
	if u.User != nil {
		if _, password := u.User.Password(); password {
			return "", "", invalid
		}
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	i := len(parts) - 2
	if i < 1 || parts[i] != "_git" {
		return "", "", invalid
	}
	project, repo := parts[i-1], strings.TrimSuffix(parts[i+1], ".git")
	if i == 1 {
		project = repo // .../{org}/_git/{repo} names the default project.
	}
	if !segment(project) || !segment(repo) {
		return "", "", invalid
	}
	return strings.ToLower(u.Hostname()), strings.ToLower(project + "/" + repo), nil
}

func finishReviewCheckout(ctx context.Context, root, objects, baseSHA, headSHA string) error {
	round := struct{ BaseSHA, HeadSHA string }{baseSHA, headSHA}
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
	// The head is a real detached worktree so Vera can build and test it. Its
	// metadata lives in the private objects.git, never the user's repository,
	// and removing the review directory removes both.
	if _, err = reviewGit(ctx, objects, "worktree", "add", "--detach", filepath.Join(input, "head"), round.HeadSHA); err != nil {
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
