package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClearedStack/waybill/internal/buildspec"
	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/muster"
	"github.com/ClearedStack/waybill/internal/spool"
	"github.com/ClearedStack/waybill/internal/worker"
)

func countersignWrap(t *testing.T, fixturePath string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	delete(rec, "item_id")
	delete(rec, "state")
	// Ensure every authority has an official public https URL in worker mode.
	if auths, ok := rec["authority"].([]any); ok {
		for _, a := range auths {
			am := a.(map[string]any)
			if _, hasURL := am["url"]; !hasURL {
				am["url"] = "https://www.acquisition.gov/far/part-97"
			}
		}
	}
	env := map[string]any{
		"outcome": "approve",
		"waybill": rec,
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestRig(t *testing.T, standin *muster.StandIn, spoolDir string) (*httptest.Server, *muster.HTTPClient, *spool.Store, *worker.Worker) {
	t.Helper()
	srv := httptest.NewServer(standin)
	t.Cleanup(srv.Close)

	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte("waybill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli, err := muster.NewHTTPClient(muster.HTTPConfig{
		BaseURL:   srv.URL,
		Contract:  "v1",
		TokenFile: tokFile,
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := spool.Open(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := worker.New(worker.Config{
		Client:            cli,
		Spool:             store,
		PollInterval:      10 * time.Millisecond,
		HeartbeatInterval: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, cli, store, w
}

func TestWorkerPriorityLanesAndAllFourProfiles(t *testing.T) {
	standin := muster.NewStandIn()
	standin.Seed([]muster.SeedItem{
		{
			ID:    "PA-0001",
			State: "approved",
			Lane:  muster.LaneBackfill,
			Next:  "waybill",
			Results: map[string]json.RawMessage{
				"countersign": countersignWrap(t, "../../tools/spec-profiles/checklist/testdata/ok-solicitation-checklist/record.json"),
			},
		},
		{
			ID:    "PA-0002",
			State: "approved",
			Lane:  muster.LaneNormal,
			Next:  "waybill",
			Results: map[string]json.RawMessage{
				"countersign": countersignWrap(t, "../../tools/spec-profiles/validator/testdata/ok-schedule-line-validator/record.json"),
			},
		},
		{
			ID:    "PA-0003",
			State: "building",
			Lane:  muster.LaneUrgent,
			Next:  "waybill",
			Results: map[string]json.RawMessage{
				"countersign": countersignWrap(t, "../../tools/spec-profiles/calculator/testdata/ok-opt-out-neutral/record.json"),
			},
		},
		{
			ID:    "PA-0004",
			State: "approved",
			Lane:  muster.LaneNormal,
			Next:  "waybill",
			Results: map[string]json.RawMessage{
				"countersign": countersignWrap(t, "../../tools/spec-profiles/decision-tree/testdata/ok-threshold-routing/record.json"),
			},
		},
	})

	_, _, _, w := newTestRig(t, standin, t.TempDir())
	ctx := context.Background()

	wantOrder := []struct {
		id    string
		lane  string
		state string
	}{
		{"PA-0003", muster.LaneUrgent, "building"},
		{"PA-0002", muster.LaneNormal, "approved"},
		{"PA-0004", muster.LaneNormal, "approved"},
		{"PA-0001", muster.LaneBackfill, "approved"},
	}

	for _, want := range wantOrder {
		rep, err := w.Step(ctx)
		if err != nil {
			t.Fatalf("Step for %s: %v", want.id, err)
		}
		if !rep.Leased || rep.ItemID != want.id || rep.Lane != want.lane || rep.Action != spool.ActionComplete || rep.State != want.state {
			t.Fatalf("got report %+v, want %+v", rep, want)
		}
		st, held, paused, results, ok := standin.ItemSnapshot(want.id)
		if !ok || st != want.state || held || paused {
			t.Fatalf("%s snapshot: state=%q held=%v paused=%v", want.id, st, held, paused)
		}
		wbRaw := results["waybill"]
		if len(wbRaw) == 0 || len(wbRaw) > worker.MaxWaybillResultBytes {
			t.Fatalf("%s results.waybill size=%d", want.id, len(wbRaw))
		}
		if buildspec.HasForbiddenObservationKey(wbRaw) {
			t.Fatalf("%s results.waybill has forbidden key: %s", want.id, wbRaw)
		}
	}

	// Queue is now empty (204 No Content).
	rep, err := w.Step(ctx)
	if err != nil || rep.Leased {
		t.Fatalf("expected idle step, got %+v, err=%v", rep, err)
	}
}

func TestWorkerCleanRoomWallIgnoresFoundAndUpstreamObservations(t *testing.T) {
	standin := muster.NewStandIn()
	standin.Seed([]muster.SeedItem{
		{
			ID:    "PA-0042",
			State: "building",
			Lane:  muster.LaneUrgent,
			Next:  "waybill",
			Found: &muster.Found{
				Source:  "web-sweep",
				Title:   "Quillmarsh Proprietary Widget <script>alert(1)</script>",
				Address: "https://secret.quillmarsh.example/pricing?plan=enterprise",
			},
			Results: map[string]json.RawMessage{
				"trawl":       json.RawMessage(`{"vendor_url":"https://secret.quillmarsh.example/pricing"}`),
				"sextant":     json.RawMessage(`{"raw_html":"<div class='qm-secret'>99999</div>","screenshot":"s3://snap"}`),
				"countersign": countersignWrap(t, "../../tools/spec-profiles/calculator/testdata/ok-opt-out-neutral/record.json"),
			},
		},
	})

	_, _, _, w := newTestRig(t, standin, t.TempDir())
	rep, err := w.Step(context.Background())
	if err != nil || rep.Action != spool.ActionComplete {
		t.Fatalf("Step: rep=%+v err=%v", rep, err)
	}

	_, _, _, results, _ := standin.ItemSnapshot("PA-0042")
	wb := string(results["waybill"])
	for _, forbidden := range []string{
		"quillmarsh",
		"Quillmarsh",
		"qm-secret",
		"99999",
		"<script>",
		"s3://snap",
	} {
		if strings.Contains(wb, forbidden) {
			t.Fatalf("results.waybill leaked %q: %s", forbidden, wb)
		}
	}
}

func TestWorkerSpoolRecoveryAcrossOutageAndRestart(t *testing.T) {
	standin := muster.NewStandIn()
	standin.Seed([]muster.SeedItem{
		{
			ID:    "PA-0050",
			State: "approved",
			Lane:  muster.LaneNormal,
			Next:  "waybill",
			Results: map[string]json.RawMessage{
				"countersign": countersignWrap(t, "../../tools/spec-profiles/calculator/testdata/ok-capped-allowance/record.json"),
			},
		},
	})

	var failComplete atomic.Bool
	failComplete.Store(true)

	mux := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/complete" && failComplete.Load() {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusServiceUnavailable)
			_, _ = rw.Write([]byte(`{"error":"E_TIMEOUT","cause":"store_slow","reason":"Logbook did not answer in time"}`))
			return
		}
		standin.ServeHTTP(rw, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	spoolDir := t.TempDir()
	cli, err := muster.NewHTTPClient(muster.HTTPConfig{
		BaseURL:  srv.URL,
		Contract: "v1",
		Token:    "waybill",
	})
	if err != nil {
		t.Fatal(err)
	}
	store1, err := spool.Open(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	w1, err := worker.New(worker.Config{Client: cli, Spool: store1})
	if err != nil {
		t.Fatal(err)
	}

	// First Step leases PA-0050, stages the compiled specification in spoolDir,
	// and hits 503 on POST /v1/complete.
	_, err = w1.Step(context.Background())
	if err == nil || !muster.IsRetryable(err) {
		t.Fatalf("expected retryable 503 error, got %v", err)
	}
	pending, err := store1.List()
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 staged item in spool, got %d (%v)", len(pending), err)
	}

	// Simulate pod restart: open a fresh spool.Store and Worker after Muster recovers.
	failComplete.Store(false)
	store2, err := spool.Open(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := worker.New(worker.Config{Client: cli, Spool: store2})
	if err != nil {
		t.Fatal(err)
	}

	rep, err := w2.Step(context.Background())
	if err != nil {
		t.Fatalf("recovery Step: %v", err)
	}
	if rep.Drained != 1 || rep.Leased {
		t.Fatalf("expected Drained=1 Leased=false, got %+v", rep)
	}
	after, err := store2.List()
	if err != nil || len(after) != 0 {
		t.Fatalf("expected empty spool after recovery, got %d (%v)", len(after), err)
	}
	st, _, _, results, ok := standin.ItemSnapshot("PA-0050")
	if !ok || st != "approved" || len(results["waybill"]) == 0 {
		t.Fatalf("PA-0050 not completed after spool replay: state=%q results=%v", st, results)
	}
}

func TestWorkerNeedsSubtoolAndDigestReplay(t *testing.T) {
	standin := muster.NewStandIn()
	standin.Seed([]muster.SeedItem{
		{
			ID:    "PA-0060",
			State: "approved",
			Lane:  muster.LaneNormal,
			Next:  "waybill",
			Results: map[string]json.RawMessage{
				"countersign": countersignWrap(t, "../../tools/spec-profiles/calculator/testdata/needs-subtool-tool-type/record.json"),
			},
		},
	})

	srv, _, _, w := newTestRig(t, standin, t.TempDir())
	rep, err := w.Step(context.Background())
	if err != nil || rep.Action != spool.ActionNeedsSubtool || rep.Code != fault.NeedsSubtool {
		t.Fatalf("Step: rep=%+v err=%v", rep, err)
	}
	st, _, paused, _, ok := standin.ItemSnapshot("PA-0060")
	if !ok || st != "needs_subtool" || !paused {
		t.Fatalf("PA-0060 snapshot: state=%q paused=%v", st, paused)
	}

	// Shipkeeper posts a new waybill image digest -> Muster replays PA-0060 back
	// to approved for waybill.
	sendHealth := func(digest string) {
		body := `{"contract":"v1","reports":[{"component":"waybill","state":"healthy","digest":"` + digest + `"}]}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/health", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer shipkeeper")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health status = %d", resp.StatusCode)
		}
	}
	sendHealth("sha256:" + strings.Repeat("1", 64))
	sendHealth("sha256:" + strings.Repeat("2", 64))

	st, _, paused, _, _ = standin.ItemSnapshot("PA-0060")
	if st != "approved" || paused {
		t.Fatalf("expected PA-0060 replayed to approved, got state=%q paused=%v", st, paused)
	}
}

func TestWorkerTerminal409RemovesStaleSpoolEntry(t *testing.T) {
	standin := muster.NewStandIn()
	_, _, store, w := newTestRig(t, standin, t.TempDir())

	_, err := store.Enqueue(spool.Entry{
		Action: spool.ActionComplete,
		Lease:  "expired-unknown-lease-token",
		ItemID: "PA-0099",
		State:  "approved",
		Result: json.RawMessage(`{"slug":"stale"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	drained, err := w.Drain(context.Background())
	if err != nil || drained != 1 {
		t.Fatalf("Drain: drained=%d err=%v", drained, err)
	}
	left, _ := store.List()
	if len(left) != 0 {
		t.Fatalf("stale 409 entry remained in spool: %+v", left)
	}
}

func TestWorkerFailsClosedAndSanitizesLogs(t *testing.T) {
	standin := muster.NewStandIn()
	standin.Seed([]muster.SeedItem{
		{
			ID:    "PA-0070",
			State: "approved",
			Lane:  muster.LaneNormal,
			Next:  "waybill",
			Results: map[string]json.RawMessage{
				"countersign": json.RawMessage(`{"outcome":"approve","raw_html":"<p>secret</p>"}`),
			},
		},
	})

	srv := httptest.NewServer(standin)
	defer srv.Close()
	cli, _ := muster.NewHTTPClient(muster.HTTPConfig{BaseURL: srv.URL, Contract: "v1", Token: "waybill"})
	store, _ := spool.Open(t.TempDir())
	var logBuf bytes.Buffer
	w, _ := worker.New(worker.Config{Client: cli, Spool: store, Log: &logBuf})

	rep, err := w.Step(context.Background())
	if err != nil || rep.Action != spool.ActionFail || rep.Code != fault.Parse {
		t.Fatalf("expected E_PARSE fail, got rep=%+v err=%v", rep, err)
	}
	if strings.Contains(logBuf.String(), "secret") || strings.Contains(logBuf.String(), "127.0.0.1") {
		t.Fatalf("worker log leaked sensitive text or host: %s", logBuf.String())
	}
}
