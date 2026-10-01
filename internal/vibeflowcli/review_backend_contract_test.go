package vibeflowcli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Captured from backend TestPRReviewCRAProtocolAcceptance on Go 1.26.5.
// Summary is the exact public response; execution retains only capability and
// public review identity, deliberately excluding private attempt/prompt fields.
func TestReviewBackendWireContract(t *testing.T) {
	summaryWire, err := os.ReadFile("testdata/pr_review_backend_summary.json")
	if err != nil {
		t.Fatal(err)
	}
	executionWire, err := os.ReadFile("testdata/pr_review_backend_execution.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/v1/vibeflow/projects/1/pr-review-summaries" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"summaries":[` + string(summaryWire) + `]}`))
	}))
	defer server.Close()
	// Vera's history pane reads summaries through this listing.
	page, err := NewClient(server.URL, "fixture").listReviewSummaries(context.Background(), 1, "")
	if err != nil || len(page.Summaries) != 1 {
		t.Fatalf("summary listing: %v %+v", err, page)
	}
	s := page.Summaries[0]
	p := s.Progress
	if s.Review.State != "changes_requested" || s.FindingCount != 1 || s.UnresolvedBlockers != 1 || s.Publication == nil || s.Publication.State != "published" || p == nil || p.ReportingVersion != 1 || !p.RequestAccepted || !p.RunnerAssigned || p.CheckoutPreparedAt == 0 || p.ReviewCompletedAt < p.CheckoutPreparedAt || !p.ResultRecorded || p.RoundNumber != 1 || p.AttemptNumber != 1 || p.State != "completed" {
		t.Fatalf("backend summary contract drift: %+v progress=%+v", s, p)
	}
	if len(s.ReviewSessions) != 1 || s.ReviewSessions[0].RoundID != p.RoundID || s.ReviewSessions[0].JobID != s.Review.ID || s.ReviewSessions[0].Active || s.ReviewSessions[0].State != "completed" {
		t.Fatalf("backend history contract drift: %+v", s.ReviewSessions)
	}
	var e reviewExecution
	if err := json.Unmarshal(executionWire, &e); err != nil {
		t.Fatal(err)
	}
	if e.Version != 2 || e.ProgressReportingVersion != 1 || e.Review.ID != s.Review.ID || e.Review.HeadSHA != p.HeadSHA || e.Review.BaseSHA != p.BaseSHA || e.Prompt != "" || e.Attempt.ID != "" {
		t.Fatal("backend capability or safe fixture contract drift")
	}
}
