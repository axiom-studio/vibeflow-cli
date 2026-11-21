package vibeflowcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// AxiomCloud's mandatory onboarding (feature #1185) has a "repo cloned" step.
// When bootstrap runs inside a git checkout, it reports the checkout's origin
// so the step can verify. The report is best effort: any failure is a warning
// and never fails bootstrap.

const cloneReportPath = "/rest/v1/onboarding/clone-report"

// cloneReportHTTPClient is replaced in tests.
var cloneReportHTTPClient = &http.Client{Timeout: 10 * time.Second}

// bootstrapCheckout returns dir's origin remote (credentials removed) and
// current branch, or "" when dir is not inside a git work tree with an origin.
func bootstrapCheckout(dir string) (remote, branch string) {
	if dir == "" {
		return "", ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", ""
	}
	remote = GetGitRemoteURL(dir)
	if remote == "" {
		return "", ""
	}
	return remote, GetGitBranch(dir)
}

// reportBootstrapClone posts the checkout in dir to VibeFlow onboarding and
// prints the outcome. It never returns an error.
func reportBootstrapClone(out io.Writer, baseURL, apiKey, dir string) {
	remote, branch := bootstrapCheckout(dir)
	if remote == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"git_remote_url": remote, "branch": branch, "cli_version": buildVersion})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(strings.TrimSpace(baseURL), "/")+cloneReportPath, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(out, "\nWarning: couldn't report this clone to VibeFlow onboarding: %s\n", terminalSafe(err.Error()))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := cloneReportHTTPClient.Do(req)
	if err != nil {
		fmt.Fprintf(out, "\nWarning: couldn't report this clone to VibeFlow onboarding: %s\n", terminalSafe(err.Error()))
		return
	}
	defer resp.Body.Close()
	var reply struct {
		Status     string   `json:"status"`
		Repository string   `json:"repository"`
		Error      string   `json:"error"`
		Linked     []string `json:"linked_repositories"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply)
	switch {
	case resp.StatusCode == http.StatusOK && reply.Status == "recorded":
		fmt.Fprintf(out, "\nReported your clone of %s to VibeFlow onboarding.\n", terminalSafe(reply.Repository))
	case resp.StatusCode == http.StatusOK:
		// Onboarding already complete: nothing to say.
	case resp.StatusCode == http.StatusNotFound:
		// A server without onboarding: nothing to say.
	case reply.Error != "" && len(reply.Linked) > 0:
		fmt.Fprintf(out, "\nVibeFlow onboarding: %s. Run bootstrap in a clone of %s.\n", terminalSafe(reply.Error), terminalSafe(strings.Join(reply.Linked, " or ")))
	case reply.Error != "":
		fmt.Fprintf(out, "\nVibeFlow onboarding: %s.\n", terminalSafe(strings.TrimSuffix(reply.Error, ".")))
	default:
		fmt.Fprintf(out, "\nWarning: couldn't report this clone to VibeFlow onboarding (HTTP %d).\n", resp.StatusCode)
	}
}

// terminalSafe makes a server-provided string safe to print: control
// characters (C0, DEL and C1, which carry ESC/CSI/OSC sequences such as an
// OSC 52 clipboard write) and invalid UTF-8 bytes (a raw 8-bit CSI) become
// U+FFFD, so the server can't drive the user's terminal (AXIOMCLOUD-4849).
func terminalSafe(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || unicode.IsControl(r) {
			r = utf8.RuneError
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}
