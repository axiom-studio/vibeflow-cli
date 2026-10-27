package vibeflowcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReviewHarnessArgsCoverEveryProvider(t *testing.T) {
	const prompt = "PROMPT"
	for _, tc := range []struct {
		key, model string
		want       []string
		stdin      bool
	}{
		{"claude", "", []string{"-p", "--dangerously-skip-permissions"}, true},
		{"claude", "M", []string{"-p", "--dangerously-skip-permissions", "--model", "M"}, true},
		{"codex", "", []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-"}, true},
		{"codex", "M", []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-m", "M", "-"}, true},
		{"gemini", "M", []string{"--yolo", "-m", "M", "-p", prompt}, false},
		{"qwen", "M", []string{"--yolo", "-m", "M", prompt}, false},
		{"copilot", "M", []string{"--yolo", "--model", "M", "-p", prompt}, false},
		{"cursor", "M", []string{"-p", "--force", "--trust", "--model", "M", prompt}, false},
		{"kiro", "", []string{"chat", "--no-interactive", "--trust-all-tools", prompt}, false},
		{"kiro", "M", []string{"chat", "--no-interactive", "--trust-all-tools", "--model", "M", prompt}, false},
	} {
		args, stdin, err := reviewHarnessArgs(tc.key, tc.model, prompt)
		if err != nil || stdin != tc.stdin || !slices.Equal(args, tc.want) {
			t.Errorf("%s %q: got %q stdin=%v err=%v", tc.key, tc.model, args, stdin, err)
		}
	}
	if _, _, err := reviewHarnessArgs("openshell", "", prompt); err == nil || !strings.Contains(err.Error(), "claude, codex, copilot, cursor, gemini, kiro, qwen") {
		t.Fatalf("unknown harness error must name supported keys: %v", err)
	}
}

// fakeReviewHarnesses installs an executable for every configured harness.
func fakeReviewHarnesses(t *testing.T, cfg *Config, script string) {
	t.Helper()
	bin := t.TempDir()
	for _, key := range reviewHarnessKeys {
		path := filepath.Join(bin, key+"-harness")
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		p := cfg.Providers[key]
		p.Binary = path
		cfg.Providers[key] = p
	}
}

func TestReviewProviderUsesNormalModeForEveryHarness(t *testing.T) {
	_, execution := reviewTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("AMBIENT_USER_SETTING", "kept")
	t.Setenv("OPENAI_API_KEY", "unselected-ambient-key")
	cfg := DefaultConfig()
	cfg.APIToken = "vibeflow-api-token-canary"
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	for _, key := range reviewHarnessKeys {
		t.Run(key, func(t *testing.T) {
			p := cfg.Providers[key]
			p.Env = map[string]string{"HARNESS_SETTING": "$AMBIENT_USER_SETTING-configured", "OPENAI_API_KEY": "selected-model-key"}
			cfg.Providers[key] = p
			root := t.TempDir()
			spec, err := prepareReviewProvider(context.Background(), cfg, key, "some-model", root, execution, &reviewBrief{Digest: strings.Repeat("a", 64)}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if spec.Binary != p.Binary || spec.Dir != filepath.Join(root, "input", "head") {
				t.Fatalf("binary %q dir %q", spec.Binary, spec.Dir)
			}
			task := filepath.Join(root, "task.txt")
			want, stdin, _ := reviewHarnessArgs(key, "some-model", "Your complete PR review task is in the file "+task+". Read that whole file first, then follow it exactly.")
			if key == "claude" { // Never the checkout's settings (issue #6392).
				want = append(slices.Clone(reviewPersonalClaudeArgs), want...)
			}
			// No credential: no VibeFlow MCP server (axiomcloud #5763).
			want = append(reviewTestNoMCPArgs(key, root), want...)
			if !slices.Equal(spec.Args, want) {
				t.Fatalf("args %q, want %q", spec.Args, want)
			}
			if (stdin && spec.InputFile != task) || (!stdin && spec.InputFile != os.DevNull) {
				t.Fatalf("input file %q", spec.InputFile)
			}
			for _, kv := range []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex"), "AMBIENT_USER_SETTING=kept", "HARNESS_SETTING=kept-configured", "OPENAI_API_KEY=selected-model-key"} {
				if !slices.Contains(spec.Env, kv) {
					t.Fatalf("normal-mode env lacks %s", kv)
				}
			}
			if trusted := slices.Contains(spec.Env, "GEMINI_CLI_TRUST_WORKSPACE=true"); trusted != (key == "gemini") {
				t.Fatalf("gemini workspace trust set=%v for %s", trusted, key)
			}
			if strings.Contains(strings.Join(spec.Args, " "), cfg.APIToken) {
				t.Fatal("VibeFlow token reached argv")
			}
			data, err := os.ReadFile(task)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{execution.Prompt, filepath.Join(root, "result.json"), filepath.Join(root, "schema.json"), "disposable git worktree", execution.Attempt.Round.HeadSHA, `"failure_reason"`} {
				if !strings.Contains(string(data), want) {
					t.Fatalf("task lacks %q", want)
				}
			}
			for _, stale := range []string{"read-only", "do not run project code"} {
				if strings.Contains(string(data), stale) {
					t.Fatalf("task still says %q", stale)
				}
			}
		})
	}
}

func TestReviewPreflightRequiresOnlyAnInstalledHarness(t *testing.T) {
	cfg := DefaultConfig()
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 1\n") // Never executed by preflight.
	for _, key := range reviewHarnessKeys {
		if err := preflightReviewProvider(context.Background(), cfg, key, ""); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	p := cfg.Providers["gemini"]
	p.Binary = filepath.Join(t.TempDir(), "missing")
	cfg.Providers["gemini"] = p
	if err := preflightReviewProvider(context.Background(), cfg, "gemini", ""); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing binary: %v", err)
	}
	if err := preflightReviewProvider(context.Background(), cfg, "unknown", ""); err == nil {
		t.Fatal("unknown harness accepted")
	}
	cfg.LLMGatewayEnabled = true
	if err := preflightReviewProvider(context.Background(), cfg, "claude", ""); err == nil {
		t.Fatal("gateway accepted missing model")
	}
	// Only Claude is relayed; another harness would silently skip the gateway.
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	if err := preflightReviewProvider(context.Background(), cfg, "codex", "gpt-5.1-codex"); err == nil || !strings.Contains(err.Error(), "codex would bypass the LLM gateway") {
		t.Fatalf("gateway codex review accepted: %v", err)
	}
	if err := preflightReviewProvider(context.Background(), cfg, "claude", "sonnet"); err != nil {
		t.Fatalf("gateway claude review refused: %v", err)
	}
}

// reviewResultEnvelope is the result.json a harness writes on success.
func reviewResultEnvelope(t *testing.T, result any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"result": result, "failure_reason": nil})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// reviewWriteResult is the fake-harness shell line that writes result.json.
// The harness runs in <root>/input/head, so the file is two levels up.
func reviewWriteResult(envelope string) string {
	return "printf '%s\\n' " + shellQuote(envelope) + " > ../../result.json\n"
}

// Messages observed from real harnesses on 2026-09-29.
func TestReviewHarnessOutputNamesUserActions(t *testing.T) {
	for output, want := range map[string]string{
		"Gemini CLI is not running in a trusted directory. To proceed, use --skip-trust":        "untrusted_workspace",
		"Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY": "authentication_required",
		"[API Error: 401 invalid access token or token expired]":                                "authentication_required",
		"Not logged in": "authentication_required",
		"IneligibleTierError: This client is no longer supported for Gemini Code Assist": "access_denied",
		"panic: index out of range": "",
	} {
		if got := classifyReviewHarnessOutput(output); got != want {
			t.Errorf("%q: got %q want %q", output, got, want)
		}
	}
}

// Where a harness has a non-interactive status command (verified 2026-09-30
// against claude 2.1.285, codex, cursor agent and kiro-cli), a logged-out
// harness is refused before Vera claims a review. Copilot, Gemini and Qwen
// have none, so they are never run.
func TestReviewHarnessLoginCheck(t *testing.T) {
	cfg := DefaultConfig()
	args := filepath.Join(t.TempDir(), "args")
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\necho \"$@\" > "+shellQuote(args)+"\nexit 1\n")
	for key, want := range map[string]string{"claude": "auth status", "codex": "login status", "cursor": "status", "kiro": "whoami"} {
		err := checkReviewHarnessLogin(context.Background(), cfg, key)
		if err == nil || !strings.Contains(err.Error(), "is not logged in") || !strings.Contains(err.Error(), reviewHarnessLoginHint(key)) {
			t.Fatalf("%s: %v", key, err)
		}
		if data, _ := os.ReadFile(args); strings.TrimSpace(string(data)) != want {
			t.Fatalf("%s ran %q", key, data)
		}
	}
	for _, key := range []string{"copilot", "gemini", "qwen"} {
		if err := checkReviewHarnessLogin(context.Background(), cfg, key); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	// cursor's agent reports a missing login with exit status 0.
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\necho 'Not logged in'\n")
	if err := checkReviewHarnessLogin(context.Background(), cfg, "cursor"); err == nil {
		t.Fatal("cursor logged out accepted")
	}
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\necho 'Logged in using ChatGPT'\n")
	for _, key := range reviewHarnessKeys {
		if err := checkReviewHarnessLogin(context.Background(), cfg, key); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	// Gateway reviews reach Claude through the VibeFlow relay, not a login.
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 1\n")
	cfg.LLMGatewayEnabled = true
	if err := checkReviewHarnessLogin(context.Background(), cfg, "claude"); err != nil {
		t.Fatal(err)
	}
}

// Claude asks whether to trust every new folder, so a review worktree is
// pre-trusted in Claude's own config and forgotten again at cleanup, keeping
// everything else in that file.
func TestClaudeReviewTrust(t *testing.T) {
	withTempRoot(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	original := `{"numStartups": 7, "oauthAccount": {"emailAddress": "x"}, "projects": {"/keep": {"hasTrustDialogAccepted": true, "allowedTools": ["a"]}}}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work", "req")
	head := filepath.Join(work, "input", "head")
	if err := os.MkdirAll(head, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CLAUDE_CONFIG_DIR": dir}
	if err := trustReviewWorktree("claude", env, head); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(head)
	var config map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	projects := config["projects"].(map[string]any)
	if entry, _ := projects[real].(map[string]any); entry == nil || entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("worktree %s not trusted: %s", real, data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0644 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	// Claude adds its own entry for the worktree's repository while it runs.
	projects[filepath.Join(filepath.Dir(filepath.Dir(real)), "objects.git")] = map[string]any{"hasTrustDialogAccepted": true}
	data, _ = json.Marshal(config)
	os.WriteFile(path, data, 0644)
	if err := forgetReviewTrust("claude", env, work); err != nil {
		t.Fatal(err)
	}
	var want, got any
	json.Unmarshal([]byte(original), &want)
	data, _ = os.ReadFile(path)
	json.Unmarshal(data, &got)
	if !equalJSON(want, got) {
		t.Fatalf("cleanup left %s", data)
	}
	// Nothing to forget: the file is not rewritten.
	before, _ := os.Stat(path)
	if err := forgetReviewTrust("claude", env, work); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("file rewritten without a change")
	}
}

// Codex keeps folder trust as [projects."<path>"] tables in config.toml.
func TestCodexReviewTrust(t *testing.T) {
	withTempRoot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "model = \"gpt\"\n\n[projects.\"/keep\"]\ntrust_level = \"trusted\"\n\n[mcp_servers.x]\ncommand = \"y\"\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work", "req")
	head := filepath.Join(work, "input", "head")
	if err := os.MkdirAll(head, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CODEX_HOME": dir}
	for range 2 { // The second call finds the entry and changes nothing.
		if err := trustReviewWorktree("codex", env, head); err != nil {
			t.Fatal(err)
		}
	}
	real, _ := filepath.EvalSymlinks(head)
	data, _ := os.ReadFile(path)
	if want := original + "\n[projects.\"" + real + "\"]\ntrust_level = \"trusted\"\n"; string(data) != want {
		t.Fatalf("config:\n%s\nwant:\n%s", data, want)
	}
	if err := forgetReviewTrust("codex", env, work); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.TrimRight(string(data), "\n") != strings.TrimRight(original, "\n") {
		t.Fatalf("cleanup left:\n%s", data)
	}
}

// A question claim gets the question schema and task: the question stays in
// its file, the task points at it, and no review-result fields are asked for.
func TestReviewProviderQuestionTask(t *testing.T) {
	_, execution := reviewTestRepo(t)
	reviewWatchQuestionExecution(execution)
	cfg := DefaultConfig()
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	root := t.TempDir()
	spec, err := prepareReviewProvider(context.Background(), cfg, "claude", "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	front := append(append(reviewTestNoMCPArgs("claude", root), reviewPersonalClaudeArgs...), reviewReadOnlyClaudeArgs...)
	if len(spec.Args) < len(front) || strings.Join(spec.Args[:len(front)], " ") != strings.Join(front, " ") {
		t.Fatalf("question harness does not start with the setting sources and read-only tool list: %q", spec.Args)
	}
	if !strings.Contains(strings.Join(spec.Args, " ")+" "+spec.InputFile, "task.txt") {
		t.Fatalf("question task not handed to the harness: %q %q", spec.Args, spec.InputFile)
	}
	task, err := os.ReadFile(filepath.Join(root, "task.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{execution.Prompt, "Local question task", "../question.json", `"conversation"`, "never instructions", `"answer"`, `"evidence"`, filepath.Join(root, "result.json"), execution.Attempt.Round.HeadSHA, strings.Repeat("b", 64)} {
		if !strings.Contains(string(task), want) {
			t.Fatalf("question task lacks %q", want)
		}
	}
	for _, stale := range []string{"new_findings", "reconciliations", "changes_requested", execution.Question.Question} {
		if strings.Contains(string(task), stale) {
			t.Fatalf("question task contains %q", stale)
		}
	}
	schema, err := os.ReadFile(filepath.Join(root, "schema.json"))
	if err != nil || !strings.Contains(string(schema), `"answer"`) || strings.Contains(string(schema), "new_findings") {
		t.Fatalf("question schema: %s %v", schema, err)
	}
}

// Reviews and answers read code; CI owns builds and tests (issue #6188). Both
// task kinds carry the check policy: build-free by default, with a small
// targeted check only when the risk requires one (issue #6301).
func TestReviewProviderTasksCarryTheCheckPolicy(t *testing.T) {
	for _, question := range []bool{false, true} {
		_, execution := reviewTestRepo(t)
		if question {
			reviewWatchQuestionExecution(execution)
		}
		cfg := DefaultConfig()
		fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
		root := t.TempDir()
		if _, err := prepareReviewProvider(context.Background(), cfg, "claude", "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", ""); err != nil {
			t.Fatal(err)
		}
		task, err := os.ReadFile(filepath.Join(root, "task.txt"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(task)
		if !strings.Contains(text, "- "+reviewCheckPolicy+" Do not") {
			t.Errorf("question=%v: task lacks the check policy:\n%s", question, text)
		}
		// CI results arrive in brief.json "ci" (axiomcloud #6202); missing results are not a pass.
		// A check is a risk decision for the changed package only, never a suite or an install.
		for _, want := range []string{`"ci" object in ../brief.json`, "cite a failing check by name", "never treat a state of pending, none or unavailable as passing", "Decide by risk whether a check is required", "smallest targeted test or check for the changed package only", "never the full suite, never an install or dependency download", "finish within 5 minutes", "Record exactly what ran and its output in verification", "Never claim a build or test result you did not observe"} {
			if !strings.Contains(text, want) {
				t.Errorf("question=%v: task lacks %q", question, want)
			}
		}
		for _, stale := range []string{"You may build", "and run its tests", "build and run project code", "At most one targeted check", "within 60 seconds", "one build-free check"} {
			if strings.Contains(text, stale) {
				t.Errorf("question=%v: task still grants %q", question, stale)
			}
		}
		// The policy sits before the result instructions, which still name the absolute result path.
		if i, j := strings.Index(text, reviewCheckPolicy), strings.Index(text, filepath.Join(root, "result.json")); i < 0 || j < i {
			t.Errorf("question=%v: policy and result path out of order", question)
		}
	}
}

// The server's per-claim execution decision (axiomcloud #4504) picks the
// check policy: allowed lets Vera build and run targeted tests within the
// attempt's remaining budget; refused forbids running any of the PR's code; a
// claim without the field (an older server) keeps the earlier policy.
func TestReviewProviderTaskFollowsExecutionDecision(t *testing.T) {
	yes, no := true, false
	var decoded reviewExecution
	if err := json.Unmarshal([]byte(`{"allow_execution":false}`), &decoded); err != nil || decoded.AllowExecution == nil || *decoded.AllowExecution {
		t.Fatalf("an explicit refusal must decode as false, not absent: %v %v", decoded.AllowExecution, err)
	}
	for _, question := range []bool{false, true} {
		for _, test := range []struct {
			name  string
			allow *bool
			want  []string
			never []string
		}{
			{"older server", nil, []string{reviewCheckPolicy}, []string{"You may build the project", "does not permit running"}},
			{"refused", &no, []string{reviewCheckPolicyRefused, "Do not build the project, install dependencies, run tests or run any program or script from this pull request", "cite a failing check by name", "Never claim a build or test result you did not observe"}, []string{reviewCheckPolicy, "You may build the project", "smallest targeted test"}},
			{"allowed", &yes, []string{"Read the \"ci\" object in ../brief.json first", "You may build the project and run targeted tests or commands inside this worktree", "confirm or refute a specific suspected defect", "minutes left", "Never install global packages", "network only for the dependency fetches the build itself needs", "each command you ran, its exit status and the key output", "Never claim a build or test result you did not observe"}, []string{reviewCheckPolicy, reviewCheckPolicyRefused}},
		} {
			_, execution := reviewTestRepo(t)
			if question {
				reviewWatchQuestionExecution(execution)
			}
			execution.AllowExecution = test.allow
			execution.Attempt.Round.DeadlineAt = time.Now().Add(25 * time.Minute).UnixMilli()
			cfg := DefaultConfig()
			fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
			root := t.TempDir()
			if _, err := prepareReviewProvider(context.Background(), cfg, "claude", "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", ""); err != nil {
				t.Fatal(err)
			}
			task, err := os.ReadFile(filepath.Join(root, "task.txt"))
			if err != nil {
				t.Fatal(err)
			}
			text := string(task)
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Errorf("question=%v %s: task lacks %q", question, test.name, want)
				}
			}
			for _, never := range test.never {
				if strings.Contains(text, never) {
					t.Errorf("question=%v %s: task carries %q", question, test.name, never)
				}
			}
			if test.allow != nil && *test.allow && !strings.Contains(text, "about 25 minutes left") {
				t.Errorf("question=%v: allowed task does not state the remaining budget:\n%s", question, text)
			}
		}
	}
}

// The budget is the attempt's effective deadline, which a local --timeout can
// shorten, and is never stated as negative.
func TestReviewExecutionBudget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	e := &reviewExecution{}
	e.Attempt.Round.DeadlineAt = now.Add(40 * time.Minute).UnixMilli()
	if got := reviewExecutionBudget(e, now); got != 40*time.Minute {
		t.Fatalf("server deadline budget %v", got)
	}
	e.budgetDeadline = now.Add(10 * time.Minute)
	if got := reviewExecutionBudget(e, now); got != 10*time.Minute {
		t.Fatalf("local cap budget %v", got)
	}
	e.budgetDeadline = now.Add(-time.Minute)
	if got := reviewExecutionBudget(e, now); got != 0 {
		t.Fatalf("expired budget %v", got)
	}
}

// The review schema offers the server's suggestion shape, and the task
// carries Vera's voice rules (issue #6215).
func TestReviewProviderTaskOffersSuggestionsAndVoice(t *testing.T) {
	_, execution := reviewTestRepo(t)
	cfg := DefaultConfig()
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	root := t.TempDir()
	if _, err := prepareReviewProvider(context.Background(), cfg, "claude", "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", ""); err != nil {
		t.Fatal(err)
	}
	task, _ := os.ReadFile(filepath.Join(root, "task.txt"))
	if !strings.Contains(string(task), "- "+reviewVoice+"\n") || !strings.Contains(reviewVoice, "Set category to security") {
		t.Fatalf("review task lacks the voice rules:\n%s", task)
	}
	schema, _ := os.ReadFile(filepath.Join(root, "schema.json"))
	for _, want := range []string{`"suggestion"`, `"start_line"`, `"end_line"`, `"replacement"`, `"RIGHT"`, `"category"`, `"security","correctness","performance","maintainability"`} {
		if !strings.Contains(string(schema), want) {
			t.Fatalf("review schema lacks %s", want)
		}
	}
}

// A server without finding_category_v1 rejects unknown fields, so the runner
// removes each finding's category before submitting to it (issue #6212).
func TestStripReviewFindingCategories(t *testing.T) {
	result := json.RawMessage(`{"schema_version":1,"new_findings":[{"key":"a","category":"security"},{"key":"b","category":null},{"key":"c"}]}`)
	out := stripReviewFindingCategories(result)
	if strings.Contains(string(out), "category") || !strings.Contains(string(out), `"key":"c"`) || !strings.Contains(string(out), `"schema_version":1`) {
		t.Fatalf("categories not stripped: %s", out)
	}
	if clean := json.RawMessage(`{"new_findings":[{"key":"c"}]}`); string(stripReviewFindingCategories(clean)) != string(clean) {
		t.Fatal("a result without categories changed")
	}
}

// One bad suggestion would make the server reject the whole result, so the
// runner drops it and keeps the finding; a valid one passes through.
func TestDropInvalidReviewSuggestions(t *testing.T) {
	head := strings.Repeat("a", 40)
	finding := func(suggestion string) string {
		return `{"key":"k","title":"t","path":"main.go","line":2,"suggestion":` + suggestion + `}`
	}
	valid := `{"revision":"` + head + `","path":"main.go","side":"RIGHT","start_line":2,"end_line":2,"replacement":"return 1"}`
	for _, tc := range []struct {
		name, suggestion string
		dropped          int
	}{
		{"valid", valid, 0},
		{"null", "null", 0},
		{"wrong revision", strings.Replace(valid, head, strings.Repeat("b", 40), 1), 1},
		{"other file", strings.Replace(valid, `"path":"main.go"`, `"path":"other.go"`, 1), 1},
		{"range misses the line", strings.Replace(valid, `"end_line":2`, `"end_line":1`, 1), 1},
		{"no replacement", strings.Replace(valid, `,"replacement":"return 1"`, "", 1), 1},
		{"LEFT side", strings.Replace(valid, "RIGHT", "LEFT", 1), 1},
	} {
		result := json.RawMessage(`{"schema_version":1,"new_findings":[` + finding(tc.suggestion) + `]}`)
		out, dropped := dropInvalidReviewSuggestions(result, head)
		if dropped != tc.dropped {
			t.Errorf("%s: dropped %d, want %d", tc.name, dropped, tc.dropped)
		}
		var parsed struct {
			Findings []map[string]json.RawMessage `json:"new_findings"`
		}
		if json.Unmarshal(out, &parsed) != nil || len(parsed.Findings) != 1 || string(parsed.Findings[0]["title"]) != `"t"` {
			t.Errorf("%s: finding not kept intact: %s", tc.name, out)
		}
		if _, has := parsed.Findings[0]["suggestion"]; has == (tc.dropped == 1) {
			t.Errorf("%s: suggestion present=%v after dropped=%d", tc.name, has, dropped)
		}
	}
}

// Isolation flags reach Claude's argv only when Vera runs isolated, in both
// the headless and the interactive launch, and never another harness's
// (issue #6195).
func TestReviewIsolationFlagsOnlyWhenChosen(t *testing.T) {
	isolateReviewHarnessHome(t) // makeReviewSpecInteractive writes folder trust.
	_, execution := reviewTestRepo(t)
	isolatedFlags := "--setting-sources " // The empty source list ends the argv string only when isolated.
	for _, tc := range []struct {
		name     string
		choice   *bool
		provider string
		want     bool
	}{
		{"not chosen", nil, "claude", false},
		{"personal", boolPtr(false), "claude", false},
		{"isolated", boolPtr(true), "claude", true},
		{"isolated codex", boolPtr(true), "codex", false},
	} {
		cfg := DefaultConfig()
		fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
		cfg.ReviewIsolated = tc.choice
		root := t.TempDir()
		spec, err := prepareReviewProvider(context.Background(), cfg, tc.provider, "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(spec.Args, "--setting-sources") && spec.Args[slices.Index(spec.Args, "--setting-sources")+1] == ""; got != tc.want {
			t.Errorf("%s headless: isolation flags present=%v, want %v: %q", tc.name, got, tc.want, spec.Args)
		}
		if err := makeReviewSpecInteractive(spec, cfg, tc.provider, "", root, false); err != nil {
			t.Fatal(err)
		}
		command := strings.Join(spec.Args, " ")
		// The empty source list must survive the shell as its own argument.
		if got := strings.Contains(command, isolatedFlags+"''"); got != tc.want {
			t.Errorf("%s interactive: isolation flags present=%v, want %v: %s", tc.name, got, tc.want, command)
		}
	}
}

func boolPtr(v bool) *bool { return &v }

// reviewTestNoMCPArgs is the argv prefix a harness gets with no VibeFlow
// credential and no MCP servers of the user's own (axiomcloud #5763).
func reviewTestNoMCPArgs(provider, root string) []string {
	switch provider {
	case "claude":
		return []string{"--mcp-config", filepath.Join(root, "mcp.json"), "--strict-mcp-config"}
	case "gemini", "qwen":
		return []string{"--allowed-mcp-server-names=" + reviewNoMCPServer}
	case "copilot":
		return []string{"--disable-mcp-server", DefaultMCPToolName}
	}
	return nil
}

// --isolated wins for one run without saving; it is refused for a harness
// that has no isolation flags; without a terminal nothing is asked.
func TestChooseReviewIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := DefaultConfig()
	cfg.APIToken = "file-token"
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatal(err)
	}
	if err := chooseReviewIsolation(cfg, path, "claude", true, true, os.Stdin, io.Discard); err != nil || !reviewIsolated(cfg) {
		t.Fatalf("--isolated: err=%v isolated=%v", err, reviewIsolated(cfg))
	}
	if saved, _ := os.ReadFile(path); strings.Contains(string(saved), "review_isolated") {
		t.Fatal("--isolated was saved; it applies to one run")
	}
	if err := chooseReviewIsolation(DefaultConfig(), path, "codex", true, true, os.Stdin, io.Discard); err == nil {
		t.Fatal("--isolated accepted for codex")
	}
	notTerminal, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer notTerminal.Close()
	fresh := DefaultConfig()
	if err := chooseReviewIsolation(fresh, path, "claude", false, true, notTerminal, io.Discard); err != nil || fresh.ReviewIsolated != nil {
		t.Fatalf("asked without a terminal: err=%v choice=%v", err, fresh.ReviewIsolated)
	}
}

// A background runner is another process that reloads config.yaml, so a
// one-run --isolated would be dropped there; it is refused instead (#6311).
func TestReviewWatchRefusesIsolatedBackgroundRunner(t *testing.T) {
	withTempRoot(t)
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = "https://example.invalid", "file-token"
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runReviewWatch(context.Background(), &out, "--background", "--isolated", "--project", "1", "--repo", t.TempDir(), "--repository-link", "7", "--provider", "claude")
	if err == nil || !strings.Contains(err.Error(), "set review_isolated: true in "+ConfigPath()) {
		t.Fatalf("--background --isolated: err=%v\n%s", err, out.String())
	}
}

// isolateReviewHarnessHome keeps folder-trust writes out of the real
// ~/.claude.json and ~/.codex/config.toml.
func isolateReviewHarnessHome(t *testing.T) {
	t.Helper()
	withTempRoot(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
}

// Personal mode keeps the user's own settings, but never the checkout's
// project or local settings: their hooks are shell commands that run before
// the model reads anything (issue #6392). Isolated mode keeps no source.
func TestReviewClaudeNeverLoadsTheCheckoutSettings(t *testing.T) {
	isolateReviewHarnessHome(t)
	_, execution := reviewTestRepo(t)
	for _, tc := range []struct {
		choice *bool
		want   string
	}{{nil, "--setting-sources user"}, {boolPtr(false), "--setting-sources user"}, {boolPtr(true), "--setting-sources ''"}} {
		cfg := DefaultConfig()
		fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
		cfg.ReviewIsolated = tc.choice
		root := t.TempDir()
		spec, err := prepareReviewProvider(context.Background(), cfg, "claude", "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		i := slices.Index(spec.Args, "--setting-sources")
		if i < 0 || i+1 == len(spec.Args) || (spec.Args[i+1] == "user") != (tc.want == "--setting-sources user") || slices.Index(spec.Args[i+1:], "--setting-sources") >= 0 {
			t.Errorf("isolated=%v headless: want %s once: %q", reviewIsolated(cfg), tc.want, spec.Args)
		}
		if err := makeReviewSpecInteractive(spec, cfg, "claude", "", root, false); err != nil {
			t.Fatal(err)
		}
		if command := strings.Join(spec.Args, " "); strings.Count(command, "--setting-sources") != 1 || !strings.Contains(command, tc.want) {
			t.Errorf("isolated=%v interactive: want %s once: %s", reviewIsolated(cfg), tc.want, command)
		}
	}
}

// The other harnesses have no switch that ignores workspace configuration
// (codex 0.163.0 started an MCP server from the checkout's .codex/config.toml
// in exec mode). A review whose head adds or changes that configuration is
// refused with a reportable reason; configuration identical to the target
// branch is the repository's own and is allowed (issue #6392).
func TestReviewRefusesHarnessConfigFromThePullRequest(t *testing.T) {
	_, execution := reviewTestRepo(t)
	cfg := DefaultConfig()
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ provider, path string }{
		{"codex", ".codex/config.toml"}, {"gemini", ".gemini/settings.json"}, {"gemini", ".env"}, {"qwen", ".qwen/settings.json"},
		{"cursor", ".cursor/hooks.json"}, {"kiro", ".kiro/settings/mcp.json"}, {"copilot", ".github/hooks/start.json"},
	} {
		root := t.TempDir()
		head, base := filepath.Join(root, "input", "head"), filepath.Join(root, "input", "base")
		write(filepath.Join(head, tc.path), "run: touch /tmp/owned")
		write(filepath.Join(base, "main.go"), "package main")
		_, err := prepareReviewProvider(context.Background(), cfg, tc.provider, "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", "")
		if reason := reviewFailureReason(err); err == nil || !strings.Contains(reason, filepath.Dir(tc.path)) || strings.Contains(reason, root) {
			t.Errorf("%s with %s added: err=%v reason=%q", tc.provider, tc.path, err, reason)
		}
		write(filepath.Join(base, tc.path), "run: touch /tmp/owned")
		if _, err := prepareReviewProvider(context.Background(), cfg, tc.provider, "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", ""); err != nil {
			t.Errorf("%s with %s unchanged from the target: %v", tc.provider, tc.path, err)
		}
		write(filepath.Join(head, tc.path), "run: something else")
		if _, err := prepareReviewProvider(context.Background(), cfg, tc.provider, "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", ""); err == nil {
			t.Errorf("%s with %s changed: not refused", tc.provider, tc.path)
		}
	}
	root := t.TempDir()
	write(filepath.Join(root, "input", "head", ".claude", "settings.json"), `{"hooks":{}}`)
	if _, err := prepareReviewProvider(context.Background(), cfg, "claude", "", root, execution, &reviewBrief{Digest: strings.Repeat("b", 64)}, "", ""); err != nil {
		t.Errorf("claude refused although its project settings never load: %v", err)
	}
}

// A symlinked configuration file is refused without being followed.
func TestReviewWorkspaceConfigRefusesSymlinks(t *testing.T) {
	root := t.TempDir()
	head, base := filepath.Join(root, "head"), filepath.Join(root, "base")
	for _, dir := range []string{filepath.Join(head, ".codex"), base} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/dev/zero", filepath.Join(head, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
	if reason := reviewFailureReason(checkReviewWorkspaceConfig("codex", head, base)); !strings.Contains(reason, ".codex/config.toml") {
		t.Fatalf("symlinked config: reason=%q", reason)
	}
}
