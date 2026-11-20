package vibeflowcli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type cloneReportServer struct {
	mu       sync.Mutex
	requests []map[string]string
	auth     []string
	paths    []string
}

func newCloneReportServer(t *testing.T, status int, reply string) (*cloneReportServer, string) {
	t.Helper()
	s := &cloneReportServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.requests, s.auth, s.paths = append(s.requests, body), append(s.auth, r.Header.Get("Authorization")), append(s.paths, r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func repoWithOrigin(t *testing.T, origin string) string {
	t.Helper()
	dir := initTestRepo(t)
	if origin != "" {
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("add origin: %s %v", out, err)
		}
	}
	return dir
}

func TestReportBootstrapClone_PostsTheSanitizedOrigin(t *testing.T) {
	srv, url := newCloneReportServer(t, http.StatusOK, `{"status":"recorded","repository":"github.com/acme/widgets"}`)
	dir := repoWithOrigin(t, "https://x-token:ghp_secret@github.com/acme/widgets.git")
	var out bytes.Buffer
	reportBootstrapClone(&out, url+"/", "vf-key", dir)

	if len(srv.requests) != 1 || srv.paths[0] != cloneReportPath || srv.auth[0] != "Bearer vf-key" {
		t.Fatalf("requests = %v paths = %v auth = %v", srv.requests, srv.paths, srv.auth)
	}
	got := srv.requests[0]
	if got["git_remote_url"] != "https://github.com/acme/widgets.git" || got["branch"] == "" || got["cli_version"] != buildVersion {
		t.Fatalf("report body = %v", got)
	}
	if strings.Contains(out.String(), "ghp_secret") || !strings.Contains(out.String(), "Reported your clone of github.com/acme/widgets") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestReportBootstrapClone_ExplainsAnUnlinkedRepository(t *testing.T) {
	_, url := newCloneReportServer(t, http.StatusConflict,
		`{"error":"github.com/acme/gadgets isn't linked to your onboarding project","linked_repositories":["github.com/acme/widgets"]}`)
	var out bytes.Buffer
	reportBootstrapClone(&out, url, "vf-key", repoWithOrigin(t, "git@github.com:acme/gadgets.git"))
	if !strings.Contains(out.String(), "github.com/acme/gadgets isn't linked") || !strings.Contains(out.String(), "Run bootstrap in a clone of github.com/acme/widgets") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestReportBootstrapClone_StaysQuietOrWarns(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		reply  string
		want   string
	}{
		"already complete": {http.StatusOK, `{"status":"complete"}`, ""},
		"older server":     {http.StatusNotFound, `404 page not found`, ""},
		"server error":     {http.StatusInternalServerError, `oops`, "Warning: couldn't report this clone to VibeFlow onboarding (HTTP 500)"},
		"rate limited":     {http.StatusTooManyRequests, `{"error":"Too many clone reports; try again in a minute"}`, "VibeFlow onboarding: Too many clone reports"},
	} {
		t.Run(name, func(t *testing.T) {
			_, url := newCloneReportServer(t, tc.status, tc.reply)
			var out bytes.Buffer
			reportBootstrapClone(&out, url, "vf-key", repoWithOrigin(t, "https://github.com/acme/widgets"))
			if tc.want == "" && out.Len() != 0 || tc.want != "" && !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output = %q, want %q", out.String(), tc.want)
			}
		})
	}

	// An unreachable server is a warning, not a failure.
	var out bytes.Buffer
	reportBootstrapClone(&out, "http://127.0.0.1:1", "vf-key", repoWithOrigin(t, "https://github.com/acme/widgets"))
	if !strings.Contains(out.String(), "Warning: couldn't report this clone") {
		t.Fatalf("unreachable server output = %q", out.String())
	}
}

func TestReportBootstrapClone_OnlyInsideACheckoutWithAnOrigin(t *testing.T) {
	srv, url := newCloneReportServer(t, http.StatusOK, `{"status":"recorded"}`)
	var out bytes.Buffer
	reportBootstrapClone(&out, url, "vf-key", t.TempDir())
	reportBootstrapClone(&out, url, "vf-key", repoWithOrigin(t, ""))
	reportBootstrapClone(&out, url, "vf-key", "")
	if len(srv.requests) != 0 || out.Len() != 0 {
		t.Fatalf("reported outside a checkout with an origin: %v %q", srv.requests, out.String())
	}
}
