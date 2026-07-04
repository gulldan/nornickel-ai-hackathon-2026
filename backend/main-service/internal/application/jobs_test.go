package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// jobService wires a HypothesisJobService over a fake hypothesis service + job store.
func jobService(cat *fakeCatalog, a *scriptedAnswerer, store *fakeJobStore) *HypothesisJobService {
	return NewHypothesisJobService(newHypService(cat, a), store)
}

// waitJob polls until the owner's job reaches a terminal status or the deadline passes.
func waitJob(t *testing.T, svc *HypothesisJobService, id string) *HypothesisJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := svc.Get(context.Background(), "alice", id)
		if err == nil && (job.Status == HypothesisJobSucceeded || job.Status == HypothesisJobFailed) {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not reach a terminal status in time")
	return nil
}

// Enqueue validates input, persists the job and runs it to a terminal status.
func TestJobs_Enqueue_VerifyRunsToSuccess(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "alpha beta gamma delta"})
	verdict := reply(`{"verdict":"supported","confidence":0.9,"rationale":"",`+
		`"supporting":["alpha beta gamma delta"],"contradicting":[]}`,
		&commonv1.Source{ChunkId: "c1", Snippet: "alpha beta gamma delta epsilon"})
	a := newAnswerer(verdict)
	store := newJobStore()
	// Park the detached runner until the freshly-returned job is inspected: the
	// runner mutates that same object once it claims the job.
	gate := make(chan struct{})
	store.claimGate = gate
	svc := jobService(cat, a, store)

	job, err := svc.Enqueue(context.Background(), "alice", HypothesisJobVerify, HypothesisJobInput{HypothesisID: "h1"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if job.Status != HypothesisJobQueued {
		t.Fatalf("new job must be queued, got %q", job.Status)
	}
	close(gate)
	done := waitJob(t, svc, job.ID)
	if done.Status != HypothesisJobSucceeded {
		t.Fatalf("verify job should succeed, got %q (%s)", done.Status, done.Error)
	}
	if len(done.ResultIDs) != 1 || done.ResultIDs[0] != "h1" {
		t.Fatalf("succeeded job must carry the result id, got %v", done.ResultIDs)
	}
}

// Enqueue must not share the returned record with the detached runner: the HTTP
// handler renders the returned job while run() executes. This renders it
// continuously for the runner's whole lifetime so -race flags any shared write.
func TestJobs_Enqueue_ReturnedJobIsRaceSafe(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "alpha beta gamma delta"})
	a := newAnswerer(reply(`{"verdict":"supported","confidence":0.9,"rationale":"",`+
		`"supporting":["alpha beta gamma delta"],"contradicting":[]}`,
		&commonv1.Source{ChunkId: "c1", Snippet: "alpha beta gamma delta epsilon"}))
	store := newJobStore() // no claimGate: the runner proceeds immediately
	svc := jobService(cat, a, store)

	job, err := svc.Enqueue(context.Background(), "alice", HypothesisJobVerify, HypothesisJobInput{HypothesisID: "h1"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, merr := json.Marshal(job); merr != nil {
					t.Errorf("marshal returned job: %v", merr)
					return
				}
			}
		}
	}()
	done := waitJob(t, svc, job.ID)
	close(stop)
	wg.Wait()

	if done.Status != HypothesisJobSucceeded {
		t.Fatalf("verify job should succeed, got %q (%s)", done.Status, done.Error)
	}
	if job.Status != HypothesisJobQueued {
		t.Fatalf("returned record must stay the queued snapshot, got %q", job.Status)
	}
}

// A failing operation marks the job failed with the error recorded.
func TestJobs_Enqueue_FailureRecorded(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	a := newAnswerer(failReply(errors.New("llm down")))
	svc := jobService(cat, a, newJobStore())
	job, err := svc.Enqueue(context.Background(), "alice", HypothesisJobVerify, HypothesisJobInput{HypothesisID: "h1"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	done := waitJob(t, svc, job.ID)
	if done.Status != HypothesisJobFailed || done.Error == "" {
		t.Fatalf("failed job must record an error, got %q / %q", done.Status, done.Error)
	}
}

// Enqueue rejects invalid input and a blank owner.
func TestJobs_Enqueue_Validation(t *testing.T) {
	svc := jobService(newCatalog(), newAnswerer(reply("{}")), newJobStore())
	if _, err := svc.Enqueue(context.Background(), "", HypothesisJobVerify, HypothesisJobInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("blank owner must be forbidden, got %v", err)
	}
	_, err := svc.Enqueue(context.Background(), "alice", HypothesisJobVerify, HypothesisJobInput{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("missing hypothesis_id must be invalid, got %v", err)
	}
	_, err = svc.Enqueue(context.Background(), "alice", "bogus", HypothesisJobInput{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown kind must be invalid, got %v", err)
	}
	_, err = svc.Enqueue(context.Background(), "alice", HypothesisJobGenerate, HypothesisJobInput{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("generate without kpi must be invalid, got %v", err)
	}
}

// An unconfigured service refuses to enqueue.
func TestJobs_Enqueue_Unconfigured(t *testing.T) {
	var svc *HypothesisJobService
	if _, err := svc.Enqueue(context.Background(), "alice", HypothesisJobVerify, HypothesisJobInput{HypothesisID: "h"}); err == nil {
		t.Fatal("nil service must refuse to enqueue")
	}
}

// Get returns ErrNotFound for an unknown job and a foreign owner.
func TestJobs_Get_NotFound(t *testing.T) {
	svc := jobService(newCatalog(), newAnswerer(reply("{}")), newJobStore())
	if _, err := svc.Get(context.Background(), "alice", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown job must be not-found, got %v", err)
	}
}

// List returns recent jobs newest-first and dedups ids.
func TestJobs_List(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "s"})
	svc := jobService(cat, newAnswerer(failReply(errors.New("x"))), newJobStore())
	job, err := svc.Enqueue(context.Background(), "alice", HypothesisJobVerify, HypothesisJobInput{HypothesisID: "h1"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitJob(t, svc, job.ID)
	jobs, err := svc.List(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("List must return the enqueued job, got %v", jobs)
	}
}

// EnsureVerify automatically starts one corpus-check job per hypothesis without
// a verdict and uses a short dedupe key so passive board refreshes do not
// enqueue the same verification task repeatedly.
func TestJobs_EnsureVerify(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "missing", OwnerId: "alice", Title: "Missing", Statement: "alpha beta gamma delta",
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "done", OwnerId: "alice", Title: "Done", Statement: "alpha beta",
		Assessment: `{"check":{"verdict":"supported"}}`,
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "foreign", OwnerId: "bob", Title: "Foreign", Statement: "alpha beta",
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "closed", OwnerId: "alice", Title: "Closed", Status: "archived", Statement: "alpha beta",
	})
	verdict := reply(`{"verdict":"supported","confidence":0.9,"rationale":"ok",`+
		`"supporting":["alpha beta"],"contradicting":[]}`,
		&commonv1.Source{ChunkId: "c1", Snippet: "alpha beta gamma delta epsilon"})
	svc := jobService(cat, newAnswerer(verdict), newJobStore())

	started := svc.EnsureVerify(context.Background(), "alice", []*commonv1.Hypothesis{
		cat.hypotheses["missing"],
		cat.hypotheses["done"],
		cat.hypotheses["foreign"],
		cat.hypotheses["closed"],
	}, 10)
	if started != 1 {
		t.Fatalf("EnsureVerify started %d jobs, want 1", started)
	}
	jobs, err := svc.List(context.Background(), "alice", 10)
	if err != nil {
		t.Fatalf("List jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Kind != HypothesisJobVerify {
		t.Fatalf("expected one verify job, got %+v", jobs)
	}
	done := waitJob(t, svc, jobs[0].ID)
	if done.Status != HypothesisJobSucceeded {
		t.Fatalf("auto verify job status = %s (%s)", done.Status, done.Error)
	}
	if got := verdictFromAssessment(cat.hypotheses["missing"].GetAssessment()); got != verdictSupported {
		t.Fatalf("missing hypothesis must be verified, got verdict %q", got)
	}
	if again := svc.EnsureVerify(context.Background(), "alice", []*commonv1.Hypothesis{cat.hypotheses["missing"]}, 10); again != 0 {
		t.Fatalf("EnsureVerify must not enqueue a duplicate, got %d", again)
	}
}

// EnsureAssessTRL automatically starts one readiness job per missing-УГТ
// hypothesis and uses a short dedupe key so passive board refreshes do not
// enqueue the same scoring task repeatedly.
func TestJobs_EnsureAssessTRL(t *testing.T) {
	cat := newCatalog()
	withTRL := int32(4)
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "missing", OwnerId: "alice", Title: "Missing", Statement: "alpha beta gamma delta",
		Assessment: `{"check":{"verdict":"supported"}}`,
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "done", OwnerId: "alice", Title: "Done", Statement: "alpha beta", Trl: &withTRL,
		Assessment: `{"check":{"verdict":"supported"}}`,
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "unverified", OwnerId: "alice", Title: "Unverified", Statement: "alpha beta",
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "foreign", OwnerId: "bob", Title: "Foreign", Statement: "alpha beta",
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "closed", OwnerId: "alice", Title: "Closed", Status: "archived", Statement: "alpha beta",
	})
	a := newAnswerer(reply(`{"levels":[{"level":1,"met":true},{"level":2,"met":false}]}`))
	svc := jobService(cat, a, newJobStore())

	started := svc.EnsureAssessTRL(context.Background(), "alice", []*commonv1.Hypothesis{
		cat.hypotheses["missing"],
		cat.hypotheses["done"],
		cat.hypotheses["unverified"],
		cat.hypotheses["foreign"],
		cat.hypotheses["closed"],
	}, 10)
	if started != 1 {
		t.Fatalf("EnsureAssessTRL started %d jobs, want 1", started)
	}
	jobs, err := svc.List(context.Background(), "alice", 10)
	if err != nil {
		t.Fatalf("List jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Kind != HypothesisJobAssessTRL {
		t.Fatalf("expected one assess_trl job, got %+v", jobs)
	}
	done := waitJob(t, svc, jobs[0].ID)
	if done.Status != HypothesisJobSucceeded {
		t.Fatalf("auto TRL job status = %s (%s)", done.Status, done.Error)
	}
	if cat.hypotheses["missing"].Trl == nil || cat.hypotheses["missing"].GetTrl() != 1 {
		t.Fatalf("missing hypothesis must be scored to УГТ 1, got %v", cat.hypotheses["missing"].Trl)
	}
	again := svc.EnsureAssessTRL(context.Background(), "alice", []*commonv1.Hypothesis{cat.hypotheses["missing"]}, 10)
	if again != 0 {
		t.Fatalf("EnsureAssessTRL must not enqueue a duplicate, got %d", again)
	}
}

// EnsureEnrich starts Stage-2 enrichment only for verified, not-yet-enriched
// hypotheses (after verify, never before), and dedupes passive board refreshes.
func TestJobs_EnsureEnrich(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "enrichable", OwnerId: "alice", Title: "E", Statement: "alpha beta gamma", Status: "generated",
		Assessment: `{"check":{"verdict":"supported"}}`,
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "unverified", OwnerId: "alice", Title: "U", Statement: "delta epsilon",
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "enriched", OwnerId: "alice", Title: "D", Statement: "zeta eta",
		Assessment: `{"check":{"verdict":"supported"},"enrichment":{"enriched_at":"2026-01-01T00:00:00Z"}}`,
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "closed", OwnerId: "alice", Title: "C", Status: "archived", Statement: "theta",
		Assessment: `{"check":{"verdict":"supported"}}`,
	})
	cat.putHypothesis(&commonv1.Hypothesis{
		Id: "foreign", OwnerId: "bob", Title: "F", Statement: "iota",
		Assessment: `{"check":{"verdict":"supported"}}`,
	})
	enrichReply := reply(`{"microstructure_mechanism":"м","causal_chain":[{"stage":"процесс","change":"x"}],` +
		`"experiment_plan":{"success_criteria":"крит"}}`)
	svc := jobService(cat, newAnswerer(enrichReply), newJobStore())

	started := svc.EnsureEnrich(context.Background(), "alice", []*commonv1.Hypothesis{
		cat.hypotheses["enrichable"],
		cat.hypotheses["unverified"],
		cat.hypotheses["enriched"],
		cat.hypotheses["closed"],
		cat.hypotheses["foreign"],
	}, 10)
	if started != 1 {
		t.Fatalf("EnsureEnrich started %d jobs, want 1", started)
	}
	jobs, err := svc.List(context.Background(), "alice", 10)
	if err != nil {
		t.Fatalf("List jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Kind != HypothesisJobEnrich {
		t.Fatalf("expected one enrich job, got %+v", jobs)
	}
	done := waitJob(t, svc, jobs[0].ID)
	if done.Status != HypothesisJobSucceeded {
		t.Fatalf("auto enrich job status = %s (%s)", done.Status, done.Error)
	}
	if !enrichmentDone(cat.hypotheses["enrichable"].GetAssessment()) {
		t.Fatal("enrichable hypothesis must carry the enrichment marker after the job")
	}
	one := []*commonv1.Hypothesis{cat.hypotheses["enrichable"]}
	if again := svc.EnsureEnrich(context.Background(), "alice", one, 10); again != 0 {
		t.Fatalf("EnsureEnrich must not enqueue a duplicate, got %d", again)
	}
}

// execute dispatches each kind to the right operation.
func TestJobs_Execute_Dispatch(t *testing.T) {
	cat := newCatalog()
	cat.kpis["kpi1"] = &commonv1.KPI{Id: "kpi1", OwnerId: "alice", Title: "T"}
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "alpha beta gamma delta", Title: "t"})
	// One reply that parses for verify/tag/etc.; generate uses the gen array.
	a := newAnswerer(reply(genArray), reply("[]"))
	svc := jobService(cat, a, newJobStore())

	ids, err := svc.execute(context.Background(), "alice", HypothesisJobGenerate, HypothesisJobInput{KPIID: "kpi1", Count: 1})
	if err != nil {
		t.Fatalf("execute generate: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("generate must return created ids")
	}

	// Unknown kind is invalid.
	if _, err := svc.execute(context.Background(), "alice", "bogus", HypothesisJobInput{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unknown kind must be invalid, got %v", err)
	}
}

// execute dispatches the single-hypothesis kinds (verify/assess_trl/competitors/
// refine/tag) to their operations, each returning the hypothesis id.
func TestJobs_Execute_SingleHypothesisKinds(t *testing.T) {
	verdict := `{"verdict":"supported","confidence":0.9,"rationale":"",` +
		`"supporting":["alpha beta gamma delta"],"contradicting":[]}`
	trl := `{"levels":[{"level":1,"met":false}]}`
	comp := `{"summary":"s","competitors":[]}`
	tag := `{"research_type":"практическое","grnti":[],"vak":[],"asjc":[]}`
	cases := []struct {
		kind   string
		answer string
	}{
		{HypothesisJobVerify, verdict},
		{HypothesisJobAssessTRL, trl},
		{HypothesisJobCompetitors, comp},
		{HypothesisJobRefine, verdict},
		{HypothesisJobTag, tag},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			cat := newCatalog()
			cat.putHypothesis(&commonv1.Hypothesis{
				Id: "h1", OwnerId: "alice", Title: "t", Statement: "alpha beta gamma delta",
			})
			svc := jobService(cat, newAnswerer(reply(tc.answer)), newJobStore())
			ids, err := svc.execute(context.Background(), "alice", tc.kind, HypothesisJobInput{HypothesisID: "h1"})
			if err != nil {
				t.Fatalf("execute %s: %v", tc.kind, err)
			}
			if len(ids) != 1 || ids[0] != "h1" {
				t.Fatalf("execute %s ids = %v, want [h1]", tc.kind, ids)
			}
		})
	}
}

// execute(generate) with an inline KPI title creates the KPI first.
func TestJobs_Execute_GenerateInlineKPI(t *testing.T) {
	cat := newCatalog()
	a := newAnswerer(reply(genArray), reply("[]"))
	svc := jobService(cat, a, newJobStore())
	ids, err := svc.execute(context.Background(), "alice", HypothesisJobGenerate,
		HypothesisJobInput{KPITitle: "New KPI", Count: 1})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("inline-KPI generate must produce hypotheses")
	}
}

// normalizeHypothesisJob reports a stale running job as failed.
func TestNormalizeHypothesisJob_StaleRunning(t *testing.T) {
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	job := &HypothesisJob{Status: HypothesisJobRunning, StartedAt: old, HeartbeatAt: old}
	got := normalizeHypothesisJob(job)
	if got.Status != HypothesisJobFailed {
		t.Fatalf("stale running job must read as failed, got %q", got.Status)
	}
	// A fresh heartbeat stays running.
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	live := &HypothesisJob{Status: HypothesisJobRunning, HeartbeatAt: fresh}
	if normalizeHypothesisJob(live).Status != HypothesisJobRunning {
		t.Fatal("a fresh heartbeat must stay running")
	}
	// Non-running jobs pass through unchanged.
	queued := &HypothesisJob{Status: HypothesisJobQueued}
	if normalizeHypothesisJob(queued).Status != HypothesisJobQueued {
		t.Fatal("queued job must pass through")
	}
}

// parseHypothesisJobRef round-trips and rejects malformed refs.
func TestParseHypothesisJobRef(t *testing.T) {
	ref := hypothesisJobRef("alice", "job1")
	owner, id, ok := parseHypothesisJobRef(ref)
	if !ok || owner != "alice" || id != "job1" {
		t.Fatalf("round-trip failed: %q %q %v", owner, id, ok)
	}
	if _, _, ok := parseHypothesisJobRef("noseparator"); ok {
		t.Fatal("a ref without a separator must be rejected")
	}
}

// StartWorker resumes queued jobs from the global list and is a no-op when unconfigured.
func TestJobs_StartWorker_ResumesQueued(t *testing.T) {
	cat := newCatalog()
	cat.putHypothesis(&commonv1.Hypothesis{Id: "h1", OwnerId: "alice", Statement: "alpha beta gamma delta"})
	store := newJobStore()
	verdict := reply(`{"verdict":"supported","confidence":0.9,"rationale":"",`+
		`"supporting":["alpha beta gamma delta"],"contradicting":[]}`,
		&commonv1.Source{ChunkId: "c1", Snippet: "alpha beta gamma delta x"})
	svc := jobService(cat, newAnswerer(verdict), store)

	// Seed a queued job directly so StartWorker (not Enqueue) drives it.
	job := &HypothesisJob{
		ID: "job1", OwnerID: "alice", Kind: HypothesisJobVerify, Status: HypothesisJobQueued,
		Input: HypothesisJobInput{HypothesisID: "h1"}, CreatedAt: utcNow(),
	}
	if err := store.SetJSON(context.Background(), hypothesisJobKey("alice", "job1"), job, time.Hour); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := store.LPush(context.Background(), hypothesisJobGlobalListKey(), hypothesisJobRef("alice", "job1")); err != nil {
		t.Fatalf("seed global list: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.StartWorker(ctx)
	waitJob(t, svc, "job1")

	// A nil/unconfigured service does not panic.
	var nilSvc *HypothesisJobService
	nilSvc.StartWorker(ctx)
}

// SummarizeHypothesisJobs aggregates succeeded jobs by kind within the window.
func TestSummarizeHypothesisJobs(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	mk := func(kind, status string, start, finish time.Duration) *HypothesisJob {
		return &HypothesisJob{Kind: kind, Status: status, StartedAt: ts(start), FinishedAt: ts(finish)}
	}
	jobs := []*HypothesisJob{
		mk(HypothesisJobGenerate, HypothesisJobSucceeded, -10*time.Minute, -9*time.Minute),  // 60s
		mk(HypothesisJobGenerate, HypothesisJobSucceeded, -20*time.Minute, -17*time.Minute), // 180s
		mk(HypothesisJobGenerate, HypothesisJobSucceeded, -30*time.Minute, -28*time.Minute), // 120s
		mk(HypothesisJobVerify, HypothesisJobSucceeded, -5*time.Minute, -5*time.Minute+30*time.Second),
		mk(HypothesisJobVerify, HypothesisJobFailed, -5*time.Minute, -4*time.Minute),    // failed
		mk(HypothesisJobEnrich, HypothesisJobSucceeded, -30*time.Hour, -25*time.Hour),   // out of window
		mk(HypothesisJobEnrich, HypothesisJobSucceeded, -2*time.Minute, -3*time.Minute), // finish < start
		{Kind: HypothesisJobTag, Status: HypothesisJobSucceeded, StartedAt: "bad", FinishedAt: ts(0)},
		nil,
	}
	got := SummarizeHypothesisJobs(jobs, now, 24*time.Hour)
	want := []HypothesisJobStat{
		{Kind: HypothesisJobGenerate, Count: 3, P50Sec: 120, MaxSec: 180},
		{Kind: HypothesisJobVerify, Count: 1, P50Sec: 30, MaxSec: 30},
	}
	if len(got) != len(want) {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stats[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if s := SummarizeHypothesisJobs(nil, now, time.Hour); len(s) != 0 {
		t.Fatalf("empty input must yield no stats, got %+v", s)
	}
}

// ListRecentAll reads jobs across owners from the global list, skipping bad refs.
func TestJobs_ListRecentAll(t *testing.T) {
	store := newJobStore()
	svc := jobService(newCatalog(), newAnswerer(), store)
	ctx := context.Background()
	for _, j := range []*HypothesisJob{
		{ID: "j1", OwnerID: "alice", Kind: HypothesisJobGenerate, Status: HypothesisJobSucceeded},
		{ID: "j2", OwnerID: "bob", Kind: HypothesisJobVerify, Status: HypothesisJobSucceeded},
	} {
		if err := store.SetJSON(ctx, hypothesisJobKey(j.OwnerID, j.ID), j, time.Hour); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := store.LPush(ctx, hypothesisJobGlobalListKey(), hypothesisJobRef(j.OwnerID, j.ID)); err != nil {
			t.Fatalf("seed list: %v", err)
		}
	}
	if err := store.LPush(ctx, hypothesisJobGlobalListKey(), "badref", hypothesisJobRef("ghost", "nope")); err != nil {
		t.Fatalf("seed junk: %v", err)
	}
	jobs := svc.ListRecentAll(ctx, 0)
	if len(jobs) != 2 || jobs[0].ID != "j2" || jobs[1].ID != "j1" {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
	var nilSvc *HypothesisJobService
	if nilSvc.ListRecentAll(ctx, 5) != nil {
		t.Fatal("nil service must return nil")
	}
}
