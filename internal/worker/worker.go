// Package worker runs Waybill's Muster contract v1 lease loop with atomic
// /var/spool/cs outbox staging and clean-room input isolation.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ClearedStack/waybill/internal/buildspec"
	"github.com/ClearedStack/waybill/internal/compile"
	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/muster"
	"github.com/ClearedStack/waybill/internal/record"
	"github.com/ClearedStack/waybill/internal/spool"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

// MaxWaybillResultBytes bounds results.waybill to 60 KiB so it fits inside both
// Chartroom's 64 KiB per-worker result cap and Muster/Logbook's 256 KiB
// Waybill complete request cap.
const MaxWaybillResultBytes = 60 << 10

// Default worker intervals.
const (
	DefaultPollInterval      = 5 * time.Second
	DefaultHeartbeatInterval = 20 * time.Second
	maxBackoffInterval       = 30 * time.Second
)

// Config configures a Worker.
type Config struct {
	Client            muster.Client
	Spool             *spool.Store
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	Log               io.Writer
}

// StepReport summarizes what happened on one call to Step.
type StepReport struct {
	Drained int
	Leased  bool
	ItemID  string
	Lane    string
	Action  string
	State   string
	Code    fault.Code
}

// Worker leases approved and rebuilding items from Muster, compiles them across
// the clean-room wall, and stages every outcome in /var/spool/cs before
// acknowledging the lease.
type Worker struct {
	client            muster.Client
	spool             *spool.Store
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	logMu             sync.Mutex
	log               io.Writer
}

// New constructs a Worker from cfg.
func New(cfg Config) (*Worker, error) {
	if cfg.Client == nil {
		return nil, errors.New("worker: nil Muster client")
	}
	if cfg.Spool == nil {
		return nil, errors.New("worker: nil spool store")
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	hb := cfg.HeartbeatInterval
	if hb <= 0 {
		hb = DefaultHeartbeatInterval
	}
	logW := cfg.Log
	if logW == nil {
		logW = io.Discard
	}
	return &Worker{
		client:            cfg.Client,
		spool:             cfg.Spool,
		pollInterval:      poll,
		heartbeatInterval: hb,
		log:               logW,
	}, nil
}

// Drain delivers any previously staged entries in /var/spool/cs/outbox in FIFO
// order before new work is leased.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	staged, err := w.spool.List()
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, item := range staged {
		if err := ctx.Err(); err != nil {
			return delivered, err
		}
		if err := w.deliver(ctx, item); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

// Step drains the local outbox and then processes at most one lease from Muster.
func (w *Worker) Step(ctx context.Context) (StepReport, error) {
	var rep StepReport
	drained, err := w.Drain(ctx)
	rep.Drained = drained
	if err != nil {
		return rep, err
	}

	grant, err := w.client.Lease(ctx)
	if err != nil {
		w.logEvent("lease_error", "", "", "", "", muster.CodeOf(err), "")
		return rep, err
	}
	if grant == nil {
		return rep, nil
	}

	rep.Leased = true
	rep.ItemID = grant.ID
	rep.Lane = grant.Lane

	stopHB := w.startHeartbeat(ctx, grant.Lease)
	defer stopHB()

	entry := w.evaluateGrant(grant)
	rep.Action = entry.Action
	rep.State = entry.State
	rep.Code = entry.Code

	name, err := w.spool.Enqueue(entry)
	if err != nil {
		return rep, err
	}
	if err := w.deliver(ctx, spool.Staged{Name: name, Entry: entry}); err != nil {
		return rep, err
	}
	return rep, nil
}

// Run loops until ctx is cancelled, draining /var/spool/cs first and backing
// off deterministically when the queue is empty or Muster is temporarily slow.
func (w *Worker) Run(ctx context.Context) error {
	backoff := w.pollInterval
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		rep, err := w.Step(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			wait := backoff
			if backoff < maxBackoffInterval {
				backoff *= 2
				if backoff > maxBackoffInterval {
					backoff = maxBackoffInterval
				}
			}
			if !sleepContext(ctx, wait) {
				return nil
			}
			continue
		}
		backoff = w.pollInterval
		if rep.Leased || rep.Drained > 0 {
			// Immediately check for more work so urgent/normal queues drain
			// without waiting for a poll tick.
			continue
		}
		if !sleepContext(ctx, w.pollInterval) {
			return nil
		}
	}
}

func (w *Worker) startHeartbeat(parent context.Context, lease string) func() {
	hbCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(w.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				_, _ = w.client.Heartbeat(hbCtx, lease)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// evaluateGrant enforces the clean-room boundary on grant, compiles the
// approved record from results.countersign only, and builds the spool.Entry to
// stage before acknowledging Muster.
//
// grant.Found and every grant.Results entry other than "countersign" are
// deliberately ignored and never read.
func (w *Worker) evaluateGrant(grant *muster.Grant) spool.Entry {
	if grant.Kind != muster.KindItem || !record.IsMusterItemID(grant.ID) {
		f := fault.Broken("id", "Waybill only accepts PA-0001 item leases")
		return failEntry(grant, f)
	}
	if grant.State != muster.StateApproved && grant.State != muster.StateBuilding {
		f := fault.Broken("state", "the leased item is not in approved or building state")
		return failEntry(grant, f)
	}

	rec, fErr := extractCountersignRecord(grant)
	if fErr != nil {
		return entryForFault(grant, fErr)
	}
	if len(rec.AuthorityLinks()) == 0 {
		return failEntry(grant, fault.Broken("authority.url", "at least one authority must carry an official public https url"))
	}

	res, wbResult, fErr := compile.CompileRecord(rec)
	if fErr != nil {
		return entryForFault(grant, fErr)
	}

	payload, encErr := buildspec.EncodeCompact(wbResult)
	if encErr != nil {
		return failEntry(grant, fault.Broken("build_spec", "the Waybill result could not be encoded"))
	}
	if len(payload) > MaxWaybillResultBytes {
		return failEntry(grant, fault.Broken("build_spec", "the Waybill result exceeds the 60 KiB size bound"))
	}

	return spool.Entry{
		Action: spool.ActionComplete,
		Lease:  grant.Lease,
		ItemID: grant.ID,
		State:  res.State,
		Result: json.RawMessage(payload),
	}
}

func entryForFault(grant *muster.Grant, f *fault.Error) spool.Entry {
	// CONTRACT.md permits POST /v1/needs_subtool from recorded, observed, and
	// approved. On a building rebuild lease, Muster refuses needs_subtool with
	// 409 move_not_allowed, so building faults fail closed with E_PARSE.
	if f.Code == fault.NeedsSubtool && grant.State == muster.StateApproved {
		return spool.Entry{
			Action:      spool.ActionNeedsSubtool,
			Lease:       grant.Lease,
			ItemID:      grant.ID,
			State:       muster.StateNeedsSubtool,
			Code:        fault.NeedsSubtool,
			Fingerprint: f.Fingerprint(),
		}
	}
	if f.Code == fault.Timeout {
		return spool.Entry{
			Action:      spool.ActionFail,
			Lease:       grant.Lease,
			ItemID:      grant.ID,
			Code:        fault.Timeout,
			Fingerprint: f.Fingerprint(),
		}
	}
	return failEntry(grant, f)
}

func failEntry(grant *muster.Grant, f *fault.Error) spool.Entry {
	return spool.Entry{
		Action:      spool.ActionFail,
		Lease:       grant.Lease,
		ItemID:      grant.ID,
		Code:        fault.Parse,
		Fingerprint: f.Fingerprint(),
	}
}

// countersignEnvelope decodes results.countersign without reading any other
// upstream worker's result. It accepts either:
//  1. An outer Countersign receipt carrying "waybill" or "record" (with optional
//     "outcome": "approve"), or
//  2. Direct Waybill record fields on results.countersign.
type countersignEnvelope struct {
	Outcome string          `json:"outcome,omitempty"`
	Waybill json.RawMessage `json:"waybill,omitempty"`
	Record  json.RawMessage `json:"record,omitempty"`
}

func extractCountersignRecord(grant *muster.Grant) (*record.Record, *fault.Error) {
	if grant.Results == nil {
		return nil, fault.Broken("results.countersign", "the Countersign result is missing")
	}
	raw, ok := grant.Results["countersign"]
	if !ok || len(raw) == 0 {
		return nil, fault.Broken("results.countersign", "the Countersign result is missing")
	}
	if len(raw) > record.MaxBytes {
		return nil, fault.Broken("results.countersign", "the Countersign result exceeds the size limit")
	}
	if buildspec.HasForbiddenObservationKey(raw) {
		return nil, fault.Broken("results.countersign", "the Countersign result contains a forbidden observation key")
	}

	var outer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outer); err != nil || outer == nil {
		return nil, fault.Broken("results.countersign", "the Countersign result is not a JSON object")
	}

	if outcomeRaw, hasOutcome := outer["outcome"]; hasOutcome {
		var outcome string
		if err := strictjson.Decode(outcomeRaw, &outcome); err != nil {
			return nil, fault.Broken("results.countersign.outcome", "the Countersign outcome is malformed")
		}
		if outcome != "approve" {
			return nil, fault.Broken("results.countersign.outcome", "the Countersign outcome is not approve")
		}
	}

	var recBytes []byte
	switch {
	case len(outer["waybill"]) > 0 && len(outer["record"]) > 0:
		return nil, fault.Broken("results.countersign", "both waybill and record keys are present")
	case len(outer["waybill"]) > 0:
		recBytes = outer["waybill"]
	case len(outer["record"]) > 0:
		recBytes = outer["record"]
	default:
		// Direct record shape on results.countersign (ignoring Countersign's own
		// receipt metadata keys if present alongside record fields).
		filtered := make(map[string]json.RawMessage, len(outer))
		for k, v := range outer {
			switch k {
			case "outcome", "reason", "decided_by", "decided_at", "receipt_id":
				continue
			default:
				filtered[k] = v
			}
		}
		var err error
		recBytes, err = json.Marshal(filtered)
		if err != nil {
			return nil, fault.Broken("results.countersign", "the Countersign record could not be encoded")
		}
	}

	var recMap map[string]json.RawMessage
	if err := json.Unmarshal(recBytes, &recMap); err != nil || recMap == nil {
		return nil, fault.Broken("results.countersign", "the approved record is not a JSON object")
	}

	if existingID, ok := recMap["item_id"]; ok {
		var id string
		if err := strictjson.Decode(existingID, &id); err != nil || id != grant.ID {
			return nil, fault.Broken("item_id", "the record item_id does not match the leased grant id")
		}
	} else {
		b, _ := json.Marshal(grant.ID)
		recMap["item_id"] = b
	}

	if existingState, ok := recMap["state"]; ok {
		var st string
		if err := strictjson.Decode(existingState, &st); err != nil || st != grant.State {
			return nil, fault.Broken("state", "the record state does not match the leased grant state")
		}
	} else {
		b, _ := json.Marshal(grant.State)
		recMap["state"] = b
	}

	if _, ok := recMap["contract_version"]; !ok {
		b, _ := json.Marshal(record.ContractVersion)
		recMap["contract_version"] = b
	}
	if _, ok := recMap["determination"]; !ok {
		recMap["determination"] = json.RawMessage(`{"decision":"build"}`)
	}

	normalized, err := json.Marshal(recMap)
	if err != nil {
		return nil, fault.Broken("results.countersign", "the normalized record could not be encoded")
	}
	return record.Parse(normalized)
}

func (w *Worker) deliver(ctx context.Context, staged spool.Staged) error {
	e := staged.Entry
	var err error
	switch e.Action {
	case spool.ActionComplete:
		_, err = w.client.Complete(ctx, e.Lease, e.State, e.Result)
	case spool.ActionNeedsSubtool:
		_, err = w.client.NeedsSubtool(ctx, e.Lease)
	case spool.ActionFail:
		_, err = w.client.Fail(ctx, e.Lease, e.Code)
	default:
		_ = w.spool.Remove(staged.Name)
		return nil
	}

	if err == nil {
		w.logEvent("delivered", e.ItemID, e.State, "", e.Action, e.Code, e.Fingerprint)
		return w.spool.Remove(staged.Name)
	}
	if muster.IsTerminalOutcomeRefusal(err) {
		w.logEvent("outcome_refused_terminal", e.ItemID, e.State, "", e.Action, muster.CodeOf(err), e.Fingerprint)
		return w.spool.Remove(staged.Name)
	}
	w.logEvent("outcome_deferred_spool", e.ItemID, e.State, "", e.Action, muster.CodeOf(err), e.Fingerprint)
	return err
}

type logRecord struct {
	Component   string     `json:"component"`
	Event       string     `json:"event"`
	ItemID      string     `json:"item_id,omitempty"`
	State       string     `json:"state,omitempty"`
	Lane        string     `json:"lane,omitempty"`
	Action      string     `json:"action,omitempty"`
	Code        fault.Code `json:"code,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
}

func (w *Worker) logEvent(event, itemID, state, lane, action string, code fault.Code, fp string) {
	if w.log == io.Discard {
		return
	}
	rec := logRecord{
		Component:   "waybill",
		Event:       event,
		ItemID:      itemID,
		State:       state,
		Lane:        lane,
		Action:      action,
		Code:        code,
		Fingerprint: fp,
	}
	b, err := buildspec.EncodeCompact(rec)
	if err != nil {
		return
	}
	w.logMu.Lock()
	defer w.logMu.Unlock()
	_, _ = w.log.Write(append(b, '\n'))
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
