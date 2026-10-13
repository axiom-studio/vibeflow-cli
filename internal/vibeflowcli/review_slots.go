package vibeflowcli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

// maxReviewSlots bounds each slot kind; every busy slot runs a full harness.
const maxReviewSlots = 8

// reviewClaimKindCapability is advertised by servers that accept a claim
// naming the kind of work it wants; slots require it.
const reviewClaimKindCapability = "claim_kind_v1"

// reviewSlotPick chooses the job this listener claims next and the claim kind
// to send. The single shared slot (SlotKind "") keeps the original rule: the
// first job for this repository, and the server decides question or review.
// An answer slot takes only questions. A review slot prefers reviews and
// takes a question only when no review is waiting.
func reviewSlotPick(o reviewWatchOptions, jobs []reviewJob) (reviewJob, string, bool) {
	var question *reviewJob
	for i := range jobs {
		job := jobs[i]
		if job.RepositoryLinkID != o.RepositoryLinkID || job.Provider != o.GitProvider {
			continue
		}
		if o.SlotKind == "" {
			return job, "", true
		}
		review := job.State == "queued" || job.State == "reviewing"
		if review && o.SlotKind == "review" {
			return job, "review", true
		}
		// The server holds a question while its PR's review is queued or running.
		if !review && job.PendingQuestions > 0 && question == nil {
			question = &jobs[i]
		}
	}
	if question != nil {
		return *question, "question", true
	}
	return reviewJob{}, "", false
}

// runReviewSlots runs one listener loop per slot in this process. Each slot is
// an ordinary review runner with its own name, state file, lock and server
// registration, so receipts and crash recovery stay per slot. They share the
// machine's review_concurrency limit. Harnesses run headless: one terminal
// cannot host several interactive sessions.
func runReviewSlots(ctx context.Context, client *Client, cfg *Config, o reviewWatchOptions, capacity *reviewCapacity, answerSlots, reviewSlots int, out io.Writer) error {
	slots := reviewSlotList(answerSlots, reviewSlots)
	for _, s := range slots {
		if len(o.Name+" · "+s.Label) > 100 {
			return fmt.Errorf("runner name is too long for slot names; use a shorter --name")
		}
	}
	fmt.Fprintf(out, "Vera slots: %d answer, %d review; review_concurrency %d caps harnesses running at once on this machine.\n", answerSlots, reviewSlots, cfg.ReviewConcurrency)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, len(slots))
	for i, s := range slots {
		opts := reviewSlotOptions(o, s)
		opts.SlotLabel = "" // In-process slots share one terminal; no window to rename.
		watch := &reviewWatch{client: client, cfg: cfg, options: opts, output: &reviewSlotWriter{mu: &mu, out: out, prefix: "[" + s.Label + "] "}, capacity: capacity}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := watch.run(ctx); err != nil {
				errs[i] = fmt.Errorf("%s: %w", slots[i].Label, err)
				cancel(errs[i]) // One failed slot stops the listener, as a single runner would.
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// reviewSlotWriter prefixes each complete line with its slot label so
// interleaved slot output stays readable.
type reviewSlotWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	prefix string
	buf    []byte
}

func (w *reviewSlotWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if _, err := fmt.Fprintf(w.out, "%s%s\n", w.prefix, w.buf[:i]); err != nil {
			return 0, err
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
