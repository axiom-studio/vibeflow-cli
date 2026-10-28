package vibeflowcli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewGitLabProviderAndRepositoryValidation(t *testing.T) {
	for _, provider := range []string{"github", "bitbucket", "azure_devops", "gitlab"} {
		if !validReviewGitProvider(provider) {
			t.Errorf("supported review provider %q was rejected", provider)
		}
	}
	for _, provider := range []string{"", "git", "github.com", "gitlab.example.com"} {
		if validReviewGitProvider(provider) {
			t.Errorf("unsupported review provider %q was accepted", provider)
		}
	}
	if !validReviewStartupRepository(reviewStartupRepository{ID: 9, Provider: "gitlab", Host: "gitlab.com", Name: "acme/platform/repo"}) {
		t.Fatal("nested GitLab project path was rejected")
	}
	for _, repo := range []reviewStartupRepository{
		{ID: 9, Provider: "gitlab", Host: "gitlab.example.com", Name: "acme/platform/repo"},
		{ID: 9, Provider: "gitlab", Host: "gitlab.com", Name: "acme/../repo"},
		{ID: 0, Provider: "gitlab", Host: "gitlab.com", Name: "acme/repo"},
	} {
		if validReviewStartupRepository(repo) {
			t.Errorf("invalid GitLab repository was accepted: %+v", repo)
		}
	}
}

func TestVeraListenerCommandPreservesGitLabProviderBinding(t *testing.T) {
	previousRoot, previousConfig := rootDir, flagConfigPath
	t.Cleanup(func() { rootDir, flagConfigPath = previousRoot, previousConfig })
	SetRootDir(t.TempDir())
	flagConfigPath = filepath.Join(RootDir(), "config.yaml")
	meta := SessionMeta{Provider: "claude", WorkingDir: t.TempDir(), Vera: &veraBinding{ProjectID: 13, RepositoryLinkID: 9, GitProvider: "gitlab", RunnerName: "gitlab-reviewer"}}
	command, err := veraListenerCommand(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "--git-provider gitlab") || strings.Contains(command, "--git-provider github") {
		t.Fatalf("TUI listener command lost the GitLab binding: %q", command)
	}
}

func TestReviewBackgroundBindingPersistsGitLabProvider(t *testing.T) {
	options := reviewWatchOptions{ProjectID: 13, RepositoryLinkID: 9, GitProvider: "gitlab", Kind: "local", Name: "gitlab-reviewer"}
	binding := reviewBackground{Enabled: true, ServerURL: "https://vibeflow.example", Options: options}
	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	var restored reviewBackground
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Options.GitProvider != "gitlab" || reviewBackgroundID(binding.ServerURL, restored.Options) != reviewBackgroundID(binding.ServerURL, options) {
		t.Fatalf("background runner lost GitLab provider binding: %+v", restored.Options)
	}
}

func TestReviewGitLabRemoteIdentitySupportsNestedGroupsAndRejectsUnsafeURLs(t *testing.T) {
	for raw, want := range map[string]string{
		"https://gitlab.com/acme/platform/repo.git":         "gitlab.com acme/platform/repo",
		"git@gitlab.com:Acme/Platform/Repo.git":             "gitlab.com acme/platform/repo",
		"ssh://git@gitlab.com/acme/platform/repo":           "gitlab.com acme/platform/repo",
		"https://gitlab.com/group/sub-group/nested/project": "gitlab.com group/sub-group/nested/project",
	} {
		host, name, err := reviewGitLabRemoteIdentity(raw)
		if err != nil || host+" "+name != want {
			t.Errorf("%s = %q %q %v, want %q", raw, host, name, err, want)
		}
	}
	for _, raw := range []string{
		"http://gitlab.com/acme/repo.git",
		"https://gitlab.example.com/acme/repo.git",
		"https://token@gitlab.com/acme/repo.git",
		"ssh://wrong@gitlab.com/acme/repo.git",
		"ssh://git:secret@gitlab.com/acme/repo.git",
		"git@gitlab.com:acme/../repo.git",
		"https://gitlab.com/acme/%2frepo.git",
		"https://gitlab.com/acme/repo.git?token=secret",
	} {
		if _, _, err := reviewGitLabRemoteIdentity(raw); err == nil {
			t.Errorf("unsafe GitLab remote was accepted: %s", raw)
		}
	}
}

func TestReviewGitLabCheckoutUsesNestedRepositoryIdentity(t *testing.T) {
	source, execution := reviewTestRepo(t)
	reviewTestGit(t, source, "remote", "set-url", "origin", "git@gitlab.com:Acme/Platform/Repo.git")
	execution.Review.Provider, execution.Review.ProviderHost, execution.Review.Number = "gitlab", "gitlab.com", 47
	execution.Attempt.Round.Details.BaseRepositoryName = "acme/platform/repo"
	execution.Attempt.Round.Details.HeadRepositoryName = "acme/platform/repo"
	execution.Attempt.Round.Details.BaseCloneURL = "https://gitlab.com/acme/platform/repo.git"
	execution.Attempt.Round.Details.HeadCloneURL = "https://gitlab.com/acme/platform/repo.git"
	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, execution, true); err != nil {
		t.Fatalf("GitLab nested checkout: %v", err)
	}
	if got := reviewTestGit(t, filepath.Join(root, "input", "head"), "rev-parse", "HEAD"); got != execution.Review.HeadSHA {
		t.Fatalf("checkout head %s, want %s", got, execution.Review.HeadSHA)
	}
	if _, err := os.Stat(filepath.Join(root, "input", "manifest.json")); err != nil {
		t.Fatalf("coverage manifest missing: %v", err)
	}
}

func TestReviewGitLabForkFetchesClaimedHeadThroughMergeRequestRef(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	upstream, execution := reviewTestRepo(t)
	bare := filepath.Join(t.TempDir(), "target.git")
	reviewTestGit(t, filepath.Dir(bare), "clone", "--bare", upstream, bare)
	reviewTestGit(t, bare, "update-ref", "refs/merge-requests/47/head", execution.Review.HeadSHA)
	reviewTestGit(t, bare, "config", "uploadpack.allowRefInWant", "true")
	reviewTestGit(t, bare, "config", "uploadpack.hideRefs", "refs/heads")
	reviewTestGit(t, bare, "config", "uploadpack.hideRefs", "refs/tags")

	source := t.TempDir()
	reviewTestGit(t, source, "init")
	reviewTestGit(t, source, "fetch", upstream, execution.Review.BaseSHA)
	reviewTestGit(t, source, "remote", "add", "origin", "git@gitlab.com:acme/platform/project.git")
	if err := exec.Command("git", "-C", source, "cat-file", "-e", execution.Review.HeadSHA+"^{commit}").Run(); err == nil {
		t.Fatal("fixture already contains the supposedly missing merge request head")
	}

	execution.Review.Provider, execution.Review.ProviderHost, execution.Review.Number = "gitlab", "gitlab.com", 47
	execution.Attempt.Round.Details.BaseRepositoryName = "acme/platform/project"
	execution.Attempt.Round.Details.HeadRepositoryName = "forks/team/project"
	execution.Attempt.Round.Details.BaseCloneURL = "https://gitlab.com/acme/platform/project.git"
	execution.Attempt.Round.Details.HeadCloneURL = "https://gitlab.com/forks/team/project.git"
	ssh := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\ncase \"$*\" in\n *\"git@gitlab.com git-upload-pack '/acme/platform/project.git'\") exec git-upload-pack " + shellQuote(bare) + " ;;\n *) exit 91 ;;\nesac\n"
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "packet-trace")
	t.Setenv("GIT_SSH_COMMAND", shellQuote(ssh))
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	t.Setenv("GIT_PROTOCOL", "version=2")
	t.Setenv("GIT_TRACE_PACKET", trace)

	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, execution, false); err != nil {
		t.Fatalf("GitLab fork checkout: %v", err)
	}
	head := filepath.Join(root, "input", "head")
	if got := reviewTestGit(t, head, "rev-parse", "HEAD"); got != execution.Review.HeadSHA {
		t.Fatalf("checkout head %s, want claimed SHA %s", got, execution.Review.HeadSHA)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "refs/merge-requests/47/head") {
		t.Fatalf("checkout did not fetch the GitLab merge request head ref: %s", data)
	}
}
