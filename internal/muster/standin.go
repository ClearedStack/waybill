package muster

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ClearedStack/waybill/internal/strictjson"
)

// DefaultStart is the deterministic starting instant used by Muster's saved
// contract examples.
var DefaultStart = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

var (
	workersSet = map[string]bool{
		"trawl":       true,
		"sextant":     true,
		"assay":       true,
		"countersign": true,
		"waybill":     true,
		"moldloft":    true,
		"shakedown":   true,
		"chartroom":   true,
		"tidegauge":   true,
		"bellbuoy":    true,
	}
	componentRepos = map[string]string{
		"logbook":     "logbook",
		"muster":      "muster",
		"trawl":       "trawl",
		"sextant":     "sextant",
		"assay":       "assay",
		"countersign": "countersign",
		"waybill":     "waybill",
		"moldloft":    "moldloft",
		"shakedown":   "shakedown",
		"chartroom":   "chartroom",
		"tidegauge":   "tidegauge",
		"bellbuoy":    "bellbuoy",
		"keelworks":   "keelworks",
		"lazarette":   "lazarette",
		"shipkeeper":  "shipkeeper",
		"plainforge":  "plainforge",
		"conn":        "clearedstack.com",
	}
	knownSources = map[string]bool{
		"sam-gov":       true,
		"github-issues": true,
		"web-sweep":     true,
		"site-form":     true,
	}
	knownHealthStates = map[string]bool{
		"healthy":  true,
		"idle":     true,
		"degraded": true,
		"paused":   true,
	}
	knownErrorCodes = map[string]bool{
		"E_TIMEOUT":       true,
		"E_PARSE":         true,
		"E_NEEDS_SUBTOOL": true,
	}
	pausableStates = map[string]bool{
		"recorded": true,
		"observed": true,
		"approved": true,
	}
	itemIDPattern   = regexp.MustCompile(`^PA-(?:[0-9]{4}|[1-9][0-9]{4,8})$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	powNoncePattern = regexp.MustCompile(`^[0-9a-f]{1,32}$`)
)

type routeRule struct {
	worker    string
	kind      string
	from      string
	to        string
	next      string
	lane      string
	afterDays int
}

var routingTable = []routeRule{
	{"trawl", KindItem, "discovered", "recorded", "sextant", "", 0},
	{"trawl", KindItem, "discovered", "rejected", "", "", 0},
	{"sextant", KindItem, "recorded", "observed", "assay", "", 0},
	{"assay", KindItem, "observed", "scored", "countersign", "", 0},
	{"assay", KindItem, "observed", "screened_out", "countersign", "", 0},
	{"countersign", KindItem, "scored", "approved", "waybill", "", 0},
	{"countersign", KindItem, "scored", "parked", "", "", 0},
	{"countersign", KindItem, "scored", "linked", "chartroom", "", 0},
	{"countersign", KindItem, "scored", "merged", "chartroom", "", 0},
	{"countersign", KindItem, "scored", "rejected", "", "", 0},
	{"countersign", KindItem, "screened_out", "observed", "assay", "", 0},
	{"waybill", KindItem, "approved", "approved", "moldloft", "", 0},
	{"moldloft", KindItem, "approved", "building", "shakedown", "", 0},
	{"shakedown", KindItem, "building", "building", "chartroom", "", 0},
	{"shakedown", KindItem, "building", "parked", "", "", 0},
	{"chartroom", KindItem, "building", "live", "tidegauge", LaneBackfill, 7},
	{"chartroom", KindItem, "linked", "linked", "", "", 0},
	{"chartroom", KindItem, "merged", "merged", "", "", 0},
	{"chartroom", KindItem, "retired", "retired", "", "", 0},
	{"tidegauge", KindItem, "live", "live", "tidegauge", LaneBackfill, 7},
	{"tidegauge", KindItem, "live", "retired", "chartroom", LaneUrgent, 0},
	{"tidegauge", KindItem, "live", "building", "waybill", LaneUrgent, 0},
	{"waybill", KindItem, "building", "building", "moldloft", "", 0},
	{"moldloft", KindItem, "building", "building", "shakedown", "", 0},

	{"countersign", KindProposal, "pending_approval", "approved", "bellbuoy", LaneUrgent, 0},
	{"countersign", KindProposal, "pending_approval", "declined", "", "", 0},
	{"bellbuoy", KindProposal, "approved", "building", "", "", 0},
}

// SeedItem seeds an item into a StandIn before calls run.
type SeedItem struct {
	ID      string                     `json:"id"`
	State   string                     `json:"state"`
	Lane    string                     `json:"lane"`
	Next    string                     `json:"next"`
	Found   *Found                     `json:"found,omitempty"`
	Results map[string]json.RawMessage `json:"results,omitempty"`
}

// OwnershipTarget maps an edit target to its owning repository and file.
type OwnershipTarget struct {
	Repository string `json:"repository"`
	File       string `json:"file"`
	Kind       string `json:"kind"`
}

type storedItem struct {
	id         string
	state      string
	found      *Found
	results    map[string]json.RawMessage
	pausedFrom string
	pausedBy   string
	pausedSeq  int
}

type storedProposal struct {
	id       string
	status   string
	proposal Proposal
	results  map[string]json.RawMessage
}

type queueStep struct {
	seq        int
	kind       string
	subjectID  string
	worker     string
	lane       string
	readyAt    time.Time
	attempt    int
	leaseToken string
	expiresAt  time.Time
	lostTokens map[string]bool
	held       bool
	incident   *Incident
}

type leaseOutcome struct {
	worker string
	call   string
	arg    string
	status int
	body   []byte
}

type healthEntry struct {
	state  string
	code   string
	digest string
}

// StandIn is an in-memory HTTP implementation of Muster's contract v1 used for
// standalone runs and for replaying Muster's saved contract examples.
type StandIn struct {
	mu            sync.Mutex
	now           time.Time
	leaseDuration time.Duration
	requireAuth   bool
	defaultCaller string
	condition     string
	seq           int
	pauseCounter  int
	maxItemNum    int
	nextPropNum   int

	items       map[string]*storedItem
	addresses   map[string]string
	proposals   map[string]*storedProposal
	steps       []*queueStep
	outcomes    map[string]leaseOutcome
	health      map[string]healthEntry
	ownership   map[string]OwnershipTarget
	dailyCounts map[string]int
}

// NewStandIn constructs a StandIn initialized at DefaultStart with the default
// settings from contract/v1/examples/README.md.
func NewStandIn() *StandIn {
	return &StandIn{
		now:           DefaultStart,
		leaseDuration: 60 * time.Second,
		requireAuth:   true,
		items:         map[string]*storedItem{},
		addresses:     map[string]string{},
		proposals:     map[string]*storedProposal{},
		outcomes:      map[string]leaseOutcome{},
		health:        map[string]healthEntry{},
		dailyCounts:   map[string]int{},
		ownership: map[string]OwnershipTarget{
			"catalog.sample-calculator.description": {
				Repository: "clearedstack.com",
				File:       "src/data/sample-catalog.json",
				Kind:       "DATA",
			},
		},
	}
}

// SetDefaultCaller allows unauthenticated local standalone calls to act as a
// fixed worker when requireAuth is disabled.
func (s *StandIn) SetDefaultCaller(caller string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requireAuth = false
	s.defaultCaller = caller
}

// Advance moves the stand-in clock forward by d.
func (s *StandIn) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

// SetCondition activates one of the fault conditions exercised by Muster's
// contract examples.
func (s *StandIn) SetCondition(cond string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.condition = cond
	if cond == "the queue says sextant is next for an item that is live" {
		s.items["PA-0001"] = &storedItem{
			id:      "PA-0001",
			state:   "live",
			results: map[string]json.RawMessage{},
		}
		s.seq++
		s.steps = append(s.steps, &queueStep{
			seq:        s.seq,
			kind:       KindItem,
			subjectID:  "PA-0001",
			worker:     "sextant",
			lane:       LaneNormal,
			readyAt:    s.now,
			lostTokens: map[string]bool{},
		})
	}
}

// Seed adds items to the stand-in store and queues their current step.
func (s *StandIn) Seed(items []SeedItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		results := map[string]json.RawMessage{}
		for k, v := range it.Results {
			results[k] = append(json.RawMessage(nil), v...)
		}
		var foundCopy *Found
		if it.Found != nil {
			fc := *it.Found
			foundCopy = &fc
			if fc.Address != "" {
				s.addresses[fc.Address] = it.ID
			}
		}
		s.items[it.ID] = &storedItem{
			id:      it.ID,
			state:   it.State,
			found:   foundCopy,
			results: results,
		}
		if num := parseItemNum(it.ID); num > s.maxItemNum {
			s.maxItemNum = num
		}
		lane := it.Lane
		if lane == "" {
			lane = LaneNormal
		}
		next := it.Next
		if next == "" {
			next = defaultWorkerForState(it.State)
		}
		if next != "" {
			s.seq++
			s.steps = append(s.steps, &queueStep{
				seq:        s.seq,
				kind:       KindItem,
				subjectID:  it.ID,
				worker:     next,
				lane:       lane,
				readyAt:    s.now,
				lostTokens: map[string]bool{},
			})
		}
	}
}

// ItemSnapshot returns an item's current state and stored worker results for
// assertions in tests.
func (s *StandIn) ItemSnapshot(id string) (state string, held, paused bool, results map[string]json.RawMessage, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, exists := s.items[id]
	if !exists {
		return "", false, false, nil, false
	}
	resCopy := make(map[string]json.RawMessage, len(it.results))
	for k, v := range it.results {
		resCopy[k] = append(json.RawMessage(nil), v...)
	}
	var isHeld bool
	for _, st := range s.steps {
		if st.kind == KindItem && st.subjectID == id && st.held {
			isHeld = true
		}
	}
	return it.state, isHeld, it.state == StateNeedsSubtool, resCopy, true
}

func parseItemNum(id string) int {
	if !strings.HasPrefix(id, "PA-") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "PA-"))
	if err != nil {
		return 0
	}
	return n
}

func defaultWorkerForState(state string) string {
	switch state {
	case "discovered":
		return "trawl"
	case "recorded":
		return "sextant"
	case "observed":
		return "assay"
	case "screened_out", "scored":
		return "countersign"
	case "approved":
		return "waybill"
	case "live":
		return "tidegauge"
	default:
		return ""
	}
}

// ServeHTTP handles every Muster contract v1 endpoint.
func (s *StandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet {
			writeRefusal(w, http.StatusMethodNotAllowed, "E_PARSE", "wrong_method", "this call does not accept that method")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.condition == "every place for a call in flight is taken" {
		writeRefusal(w, http.StatusServiceUnavailable, "E_TIMEOUT", "busy", "too busy; try again")
		return
	}

	knownPath, wantMethod := endpointMethod(r.URL.Path)
	if !knownPath {
		writeRefusal(w, http.StatusNotFound, "E_PARSE", "no_such_call", "no such call")
		return
	}
	if r.Method != wantMethod {
		writeRefusal(w, http.StatusMethodNotAllowed, "E_PARSE", "wrong_method", "this call does not accept that method")
		return
	}

	if s.condition == "the cluster does not answer the identity check in time" {
		writeRefusal(w, http.StatusServiceUnavailable, "E_TIMEOUT", "busy", "the identity check took too long; try again")
		return
	}

	caller, ok := s.authenticate(r)
	if !ok {
		writeRefusal(w, http.StatusUnauthorized, "E_PARSE", "no_credential", "no valid credential was presented")
		return
	}
	if !authorizedCaller(r.URL.Path, caller) {
		writeRefusal(w, http.StatusForbidden, "E_PARSE", "not_your_call", "this caller may not make this call")
		return
	}

	if r.URL.Path == "/v1/feed" {
		s.handleFeed(w)
		return
	}

	ct := r.Header.Get("Content-Type")
	mediaType := strings.TrimSpace(strings.Split(ct, ";")[0])
	if !strings.EqualFold(mediaType, "application/json") {
		writeRefusal(w, http.StatusUnsupportedMediaType, "E_PARSE", "wrong_type", "the content type must be application/json")
		return
	}

	limit := s.bodyLimit(r.URL.Path, caller)
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "not_json", "the request is not one json value")
		return
	}
	if len(body) > limit {
		writeRefusal(w, http.StatusRequestEntityTooLarge, "E_PARSE", "too_large", "the request is too large")
		return
	}

	fields, ok := decodeObject(body)
	if !ok {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "not_json", "the request is not one json value")
		return
	}

	switch r.URL.Path {
	case "/v1/lease":
		s.handleLease(w, caller, fields)
	case "/v1/heartbeat":
		s.handleHeartbeat(w, caller, fields)
	case "/v1/complete":
		s.handleComplete(w, caller, fields)
	case "/v1/fail":
		s.handleFail(w, caller, fields)
	case "/v1/needs_subtool":
		s.handleNeedsSubtool(w, caller, fields)
	case "/v1/release":
		s.handleRelease(w, fields)
	case "/v1/items":
		s.handleItems(w, caller, fields)
	case "/v1/health":
		s.handleHealth(w, fields)
	case "/v1/edits":
		s.handleEdits(w, fields)
	}
}

func endpointMethod(path string) (bool, string) {
	switch path {
	case "/v1/feed":
		return true, http.MethodGet
	case "/v1/lease", "/v1/heartbeat", "/v1/complete", "/v1/fail", "/v1/needs_subtool",
		"/v1/items", "/v1/release", "/v1/health", "/v1/edits":
		return true, http.MethodPost
	default:
		return false, ""
	}
}

func (s *StandIn) authenticate(r *http.Request) (string, bool) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		if !s.requireAuth && s.defaultCaller != "" {
			return s.defaultCaller, true
		}
		return "", false
	}
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	if workersSet[tok] || tok == "conn" || tok == "shipkeeper" {
		return tok, true
	}
	return "", false
}

func authorizedCaller(path, caller string) bool {
	switch path {
	case "/v1/lease", "/v1/heartbeat", "/v1/complete", "/v1/fail", "/v1/needs_subtool":
		return workersSet[caller]
	case "/v1/items":
		return caller == "trawl" || caller == "conn"
	case "/v1/release":
		return caller == "countersign"
	case "/v1/health":
		return caller == "shipkeeper"
	case "/v1/edits", "/v1/feed":
		return caller == "conn"
	default:
		return false
	}
}

func (s *StandIn) bodyLimit(path, caller string) int {
	if path == "/v1/edits" {
		return 4 << 10
	}
	if path == "/v1/complete" && caller == "waybill" {
		return MaxWaybillCompleteBytes
	}
	return MaxWorkerRequestBytes
}

func decodeObject(raw []byte) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := strictjson.Decode(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func checkAllowedFields(w http.ResponseWriter, fields map[string]json.RawMessage, allowed ...string) bool {
	set := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		set[k] = true
	}
	for k := range fields {
		if !set[k] {
			writeRefusal(w, http.StatusBadRequest, "E_PARSE", "unknown_field", "the request has a field this call does not define")
			return false
		}
	}
	return true
}

func requireString(w http.ResponseWriter, fields map[string]json.RawMessage, key string) (string, bool) {
	raw, exists := fields[key]
	if !exists {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "missing_field", "a required field is missing")
		return "", false
	}
	var val string
	if err := json.Unmarshal(raw, &val); err != nil {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return "", false
	}
	if val == "" {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "missing_field", "a required field is missing")
		return "", false
	}
	return val, true
}

func checkContract(w http.ResponseWriter, fields map[string]json.RawMessage) bool {
	c, ok := requireString(w, fields, "contract")
	if !ok {
		return false
	}
	if c != ContractVersion {
		writeRefusal(w, http.StatusUnprocessableEntity, "E_NEEDS_SUBTOOL", "unknown_contract", "this contract version is not known")
		return false
	}
	return true
}

func (s *StandIn) checkStoreConditions(w http.ResponseWriter) bool {
	switch s.condition {
	case "logbook takes too long":
		writeRefusal(w, http.StatusServiceUnavailable, "E_TIMEOUT", "store_slow", "the record store took too long")
		return false
	case "logbook refuses a field":
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "the record store refused a field")
		return false
	case "logbook has no rule for a value":
		writeRefusal(w, http.StatusUnprocessableEntity, "E_NEEDS_SUBTOOL", "unknown_value", "the record store has no rule for a value")
		return false
	case "logbook fails in a way nothing provides for":
		writeRefusal(w, http.StatusInternalServerError, "E_PARSE", "unexpected", "an unexpected condition; the request was refused")
		return false
	}
	return true
}

func (s *StandIn) expireLeases() {
	for _, st := range s.steps {
		if st.leaseToken != "" && !s.now.Before(st.expiresAt) {
			// A lease remains usable until someone else takes the step; mark it
			// expired by leaving leaseToken until re-offered, or check expiresAt.
		}
	}
}

func laneRank(lane string) int {
	switch lane {
	case LaneUrgent:
		return 0
	case LaneNormal:
		return 1
	case LaneBackfill:
		return 2
	default:
		return 3
	}
}

func workerHoldsSubject(worker, kind, state string) bool {
	if kind == KindIncident {
		return worker == "bellbuoy"
	}
	for _, r := range routingTable {
		if r.worker == worker && r.kind == kind && r.from == state {
			return true
		}
	}
	return false
}

func (s *StandIn) handleLease(w http.ResponseWriter, caller string, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract") || !checkContract(w, fields) {
		return
	}
	if !s.checkStoreConditions(w) {
		return
	}

	var best *queueStep
	for _, st := range s.steps {
		if st.worker != caller || st.held || s.now.Before(st.readyAt) {
			continue
		}
		if st.leaseToken != "" && s.now.Before(st.expiresAt) {
			continue
		}
		if best == nil || laneRank(st.lane) < laneRank(best.lane) ||
			(laneRank(st.lane) == laneRank(best.lane) && st.seq < best.seq) {
			best = st
		}
	}
	if best == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	subjState := s.subjectState(best)
	if !workerHoldsSubject(caller, best.kind, subjState) {
		best.held = true
		best.leaseToken = ""
		s.queueIncident(best.subjectID, caller, "E_PARSE", subjState)
		writeRefusal(w, http.StatusInternalServerError, "E_PARSE", "bad_record", "a stored record was not consistent and has been set aside")
		return
	}

	if best.leaseToken != "" {
		best.lostTokens[best.leaseToken] = true
	}
	best.attempt++
	prefix := best.subjectID
	if best.kind == KindIncident {
		prefix = "incident:" + best.subjectID
	}
	best.leaseToken = fmt.Sprintf("%s.%d.%d", prefix, best.seq, best.attempt)
	best.expiresAt = s.now.Add(s.leaseDuration)

	type leaseResp struct {
		Lease    string                     `json:"lease"`
		Kind     string                     `json:"kind"`
		ID       string                     `json:"id"`
		State    string                     `json:"state"`
		Lane     string                     `json:"lane"`
		Attempt  int                        `json:"attempt"`
		Expires  string                     `json:"expires"`
		Found    *Found                     `json:"found,omitempty"`
		Results  map[string]json.RawMessage `json:"results,omitempty"`
		Proposal *Proposal                  `json:"proposal,omitempty"`
		Incident *Incident                  `json:"incident,omitempty"`
	}

	resp := leaseResp{
		Lease:   best.leaseToken,
		Kind:    best.kind,
		ID:      best.subjectID,
		State:   subjState,
		Lane:    best.lane,
		Attempt: best.attempt,
		Expires: best.expiresAt.UTC().Format(time.RFC3339),
	}

	switch best.kind {
	case KindItem:
		it := s.items[best.subjectID]
		if it.found != nil {
			fc := *it.found
			resp.Found = &fc
		}
		if len(it.results) > 0 {
			resp.Results = it.results
		}
	case KindProposal:
		pr := s.proposals[best.subjectID]
		pc := pr.proposal
		resp.Proposal = &pc
		if len(pr.results) > 0 {
			resp.Results = pr.results
		}
	case KindIncident:
		ic := *best.incident
		resp.Incident = &ic
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *StandIn) subjectState(st *queueStep) string {
	switch st.kind {
	case KindItem:
		if it, ok := s.items[st.subjectID]; ok {
			return it.state
		}
	case KindProposal:
		if pr, ok := s.proposals[st.subjectID]; ok {
			return pr.status
		}
	case KindIncident:
		if st.incident != nil {
			return st.incident.State
		}
	}
	return ""
}

func (s *StandIn) findActiveStep(caller, token string) (*queueStep, bool) {
	for _, st := range s.steps {
		if st.worker == caller && st.leaseToken == token && !st.held && !st.lostTokens[token] {
			return st, true
		}
	}
	return nil, false
}

func (s *StandIn) removeStep(target *queueStep) {
	out := s.steps[:0]
	for _, st := range s.steps {
		if st != target {
			out = append(out, st)
		}
	}
	s.steps = out
}

func (s *StandIn) handleHeartbeat(w http.ResponseWriter, caller string, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "lease") || !checkContract(w, fields) {
		return
	}
	lease, ok := requireString(w, fields, "lease")
	if !ok {
		return
	}
	if !s.checkStoreConditions(w) {
		return
	}
	st, ok := s.findActiveStep(caller, lease)
	if !ok || !s.now.Before(st.expiresAt) {
		writeRefusal(w, http.StatusConflict, "E_PARSE", "lease_not_held", "the lease is not held")
		return
	}
	st.expiresAt = s.now.Add(s.leaseDuration)
	writeJSON(w, http.StatusOK, HeartbeatAck{
		Lease:   lease,
		Expires: st.expiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *StandIn) handleComplete(w http.ResponseWriter, caller string, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "lease", "state", "result") || !checkContract(w, fields) {
		return
	}
	lease, ok := requireString(w, fields, "lease")
	if !ok {
		return
	}
	toState, ok := requireString(w, fields, "state")
	if !ok {
		return
	}
	var resultRaw json.RawMessage
	if raw, hasResult := fields["result"]; hasResult {
		var obj map[string]json.RawMessage
		if err := strictjson.Decode(raw, &obj); err != nil || obj == nil {
			writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
			return
		}
		resultRaw = append(json.RawMessage(nil), raw...)
	}
	if !s.checkStoreConditions(w) {
		return
	}

	if out, exists := s.outcomes[lease]; exists && out.worker == caller && out.call == "complete" && out.arg == toState {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(out.status)
		_, _ = w.Write(out.body)
		return
	}

	st, ok := s.findActiveStep(caller, lease)
	if !ok {
		writeRefusal(w, http.StatusConflict, "E_PARSE", "lease_not_held", "the lease is not held")
		return
	}

	if st.kind == KindIncident {
		if len(resultRaw) > 0 {
			writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
			return
		}
		if toState != st.incident.State {
			writeRefusal(w, http.StatusConflict, "E_PARSE", "move_not_allowed", "that move is not allowed")
			return
		}
		s.removeStep(st)
		resp := CompleteAck{ID: st.subjectID, State: toState}
		body := mustJSON(resp)
		s.outcomes[lease] = leaseOutcome{worker: caller, call: "complete", arg: toState, status: http.StatusOK, body: body}
		writeRawJSON(w, http.StatusOK, body)
		return
	}

	fromState := s.subjectState(st)
	route, found := findRoute(caller, st.kind, fromState, toState)
	if !found {
		writeRefusal(w, http.StatusConflict, "E_PARSE", "move_not_allowed", "that move is not allowed")
		return
	}

	switch st.kind {
	case KindItem:
		it := s.items[st.subjectID]
		it.state = toState
		if len(resultRaw) > 0 {
			it.results[caller] = resultRaw
		}
	case KindProposal:
		pr := s.proposals[st.subjectID]
		pr.status = toState
		if len(resultRaw) > 0 {
			pr.results[caller] = resultRaw
		}
	}

	s.removeStep(st)
	if route.next != "" {
		nextLane := st.lane
		if route.lane != "" {
			nextLane = route.lane
		}
		readyAt := s.now
		if route.afterDays > 0 {
			readyAt = s.now.Add(time.Duration(route.afterDays) * 24 * time.Hour)
		}
		s.seq++
		s.steps = append(s.steps, &queueStep{
			seq:        s.seq,
			kind:       st.kind,
			subjectID:  st.subjectID,
			worker:     route.next,
			lane:       nextLane,
			readyAt:    readyAt,
			lostTokens: map[string]bool{},
		})
	}

	resp := CompleteAck{ID: st.subjectID, State: toState, Next: route.next}
	body := mustJSON(resp)
	s.outcomes[lease] = leaseOutcome{worker: caller, call: "complete", arg: toState, status: http.StatusOK, body: body}
	writeRawJSON(w, http.StatusOK, body)
}

func findRoute(worker, kind, from, to string) (routeRule, bool) {
	for _, r := range routingTable {
		if r.worker == worker && r.kind == kind && r.from == from && r.to == to {
			return r, true
		}
	}
	return routeRule{}, false
}

func (s *StandIn) handleFail(w http.ResponseWriter, caller string, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "lease", "code") || !checkContract(w, fields) {
		return
	}
	lease, ok := requireString(w, fields, "lease")
	if !ok {
		return
	}
	code, ok := requireString(w, fields, "code")
	if !ok {
		return
	}
	if code == "E_NEEDS_SUBTOOL" {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "report a missing rule with the needs_subtool call")
		return
	}
	if code != "E_PARSE" && code != "E_TIMEOUT" {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return
	}
	if !s.checkStoreConditions(w) {
		return
	}

	if out, exists := s.outcomes[lease]; exists && out.worker == caller && out.call == "fail" && out.arg == code {
		writeRawJSON(w, out.status, out.body)
		return
	}

	st, ok := s.findActiveStep(caller, lease)
	if !ok {
		writeRefusal(w, http.StatusConflict, "E_PARSE", "lease_not_held", "the lease is not held")
		return
	}

	subjState := s.subjectState(st)
	st.lostTokens[lease] = true
	st.leaseToken = ""
	st.expiresAt = time.Time{}

	var resp FailAck
	if code == "E_TIMEOUT" {
		resp = FailAck{ID: st.subjectID, State: subjState, Next: caller}
	} else {
		st.held = true
		if st.kind == KindItem {
			s.queueIncident(st.subjectID, caller, "E_PARSE", subjState)
		}
		resp = FailAck{ID: st.subjectID, State: subjState, Held: true}
	}

	body := mustJSON(resp)
	s.outcomes[lease] = leaseOutcome{worker: caller, call: "fail", arg: code, status: http.StatusOK, body: body}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *StandIn) handleNeedsSubtool(w http.ResponseWriter, caller string, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "lease") || !checkContract(w, fields) {
		return
	}
	lease, ok := requireString(w, fields, "lease")
	if !ok {
		return
	}
	if !s.checkStoreConditions(w) {
		return
	}

	if out, exists := s.outcomes[lease]; exists && out.worker == caller && out.call == "needs_subtool" {
		writeRawJSON(w, out.status, out.body)
		return
	}

	st, ok := s.findActiveStep(caller, lease)
	if !ok {
		writeRefusal(w, http.StatusConflict, "E_PARSE", "lease_not_held", "the lease is not held")
		return
	}

	subjState := s.subjectState(st)
	if st.kind != KindItem || !pausableStates[subjState] {
		writeRefusal(w, http.StatusConflict, "E_PARSE", "move_not_allowed", "that move is not allowed")
		return
	}

	it := s.items[st.subjectID]
	it.pausedFrom = subjState
	it.pausedBy = caller
	s.pauseCounter++
	it.pausedSeq = s.pauseCounter
	it.state = StateNeedsSubtool

	s.removeStep(st)
	s.queueIncident(it.id, caller, "E_NEEDS_SUBTOOL", subjState)

	resp := NeedsSubtoolAck{ID: it.id, State: StateNeedsSubtool}
	body := mustJSON(resp)
	s.outcomes[lease] = leaseOutcome{worker: caller, call: "needs_subtool", status: http.StatusOK, body: body}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *StandIn) queueIncident(itemID, worker, code, state string) {
	out := s.steps[:0]
	for _, st := range s.steps {
		if st.kind == KindIncident && st.subjectID == itemID {
			continue
		}
		out = append(out, st)
	}
	s.steps = out
	s.seq++
	s.steps = append(s.steps, &queueStep{
		seq:        s.seq,
		kind:       KindIncident,
		subjectID:  itemID,
		worker:     "bellbuoy",
		lane:       LaneNormal,
		readyAt:    s.now,
		lostTokens: map[string]bool{},
		incident: &Incident{
			ID:     itemID,
			Worker: worker,
			Code:   code,
			State:  state,
		},
	})
}

func (s *StandIn) handleRelease(w http.ResponseWriter, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "id") || !checkContract(w, fields) {
		return
	}
	id, ok := requireString(w, fields, "id")
	if !ok {
		return
	}
	if !itemIDPattern.MatchString(id) {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return
	}
	if !s.checkStoreConditions(w) {
		return
	}

	var heldStep *queueStep
	for _, st := range s.steps {
		if st.kind == KindItem && st.subjectID == id && st.held {
			heldStep = st
			break
		}
	}
	if heldStep == nil {
		writeRefusal(w, http.StatusConflict, "E_PARSE", "move_not_allowed", "that move is not allowed")
		return
	}

	s.removeStep(heldStep)
	s.seq++
	s.steps = append(s.steps, &queueStep{
		seq:        s.seq,
		kind:       KindItem,
		subjectID:  id,
		worker:     heldStep.worker,
		lane:       LaneUrgent,
		readyAt:    s.now,
		lostTokens: map[string]bool{},
	})

	it := s.items[id]
	writeJSON(w, http.StatusOK, ReleaseAck{
		ID:    id,
		State: it.state,
		Next:  heldStep.worker,
	})
}

func (s *StandIn) handleItems(w http.ResponseWriter, caller string, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "source", "title", "address", "result", "pow", "visitor") || !checkContract(w, fields) {
		return
	}
	source, ok := requireString(w, fields, "source")
	if !ok {
		return
	}
	title, ok := requireString(w, fields, "title")
	if !ok {
		return
	}
	address, ok := requireString(w, fields, "address")
	if !ok {
		return
	}
	if !knownSources[source] {
		writeRefusal(w, http.StatusUnprocessableEntity, "E_NEEDS_SUBTOOL", "unknown_value", "this source is not known")
		return
	}
	if len(title) > 200 || strings.ContainsAny(title, "\r\n") || len(address) > 2000 || strings.ContainsAny(address, " \t\r\n") {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return
	}
	cleanTitle := stripMarkup(title)
	if strings.TrimSpace(cleanTitle) == "" {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "nothing is left of the text once markup is removed")
		return
	}

	var resultRaw json.RawMessage
	if raw, hasResult := fields["result"]; hasResult {
		var obj map[string]json.RawMessage
		if err := strictjson.Decode(raw, &obj); err != nil || obj == nil {
			writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
			return
		}
		resultRaw = append(json.RawMessage(nil), raw...)
	}

	_, hasPoW := fields["pow"]
	_, hasVisitor := fields["visitor"]
	if source == "site-form" {
		pow, ok := requireString(w, fields, "pow")
		if !ok {
			return
		}
		visitor, ok := requireString(w, fields, "visitor")
		if !ok {
			return
		}
		if !s.verifyPoW(pow, source, title, address) {
			writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "the proof of work is not valid")
			return
		}
		if !s.allowDailySubmission(visitor) {
			writeRefusal(w, http.StatusTooManyRequests, "E_TIMEOUT", "rate_limited", "too many submissions from this visitor today; try again tomorrow")
			return
		}
	} else if hasPoW || hasVisitor {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return
	}

	if !s.checkStoreConditions(w) {
		return
	}

	if existingID, seen := s.addresses[address]; seen {
		existing := s.items[existingID]
		writeJSON(w, http.StatusOK, struct {
			ID        string `json:"id"`
			State     string `json:"state"`
			Duplicate bool   `json:"duplicate"`
		}{
			ID:        existing.id,
			State:     existing.state,
			Duplicate: true,
		})
		return
	}

	s.maxItemNum++
	id := fmt.Sprintf("PA-%04d", s.maxItemNum)
	results := map[string]json.RawMessage{}
	if len(resultRaw) > 0 {
		results[caller] = resultRaw
	}
	s.items[id] = &storedItem{
		id:      id,
		state:   "discovered",
		found:   &Found{Source: source, Title: cleanTitle, Address: address},
		results: results,
	}
	s.addresses[address] = id

	lane := LaneNormal
	if source == "web-sweep" {
		lane = LaneBackfill
	}
	s.seq++
	s.steps = append(s.steps, &queueStep{
		seq:        s.seq,
		kind:       KindItem,
		subjectID:  id,
		worker:     "trawl",
		lane:       lane,
		readyAt:    s.now,
		lostTokens: map[string]bool{},
	})

	writeJSON(w, http.StatusCreated, struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Next  string `json:"next"`
	}{
		ID:    id,
		State: "discovered",
		Next:  "trawl",
	})
}

func (s *StandIn) handleHealth(w http.ResponseWriter, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "reports") || !checkContract(w, fields) {
		return
	}
	rawReports, exists := fields["reports"]
	if !exists {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "missing_field", "a required field is missing")
		return
	}
	var reportObjs []map[string]json.RawMessage
	if err := strictjson.Decode(rawReports, &reportObjs); err != nil {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return
	}
	if len(reportObjs) == 0 {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "missing_field", "a required field is missing")
		return
	}
	if len(reportObjs) > 64 {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return
	}

	type parsedReport struct {
		component string
		state     string
		code      string
		digest    string
	}
	parsed := make([]parsedReport, 0, len(reportObjs))
	seen := map[string]bool{}
	for _, ro := range reportObjs {
		if !checkAllowedFields(w, ro, "component", "state", "code", "digest") {
			return
		}
		comp, ok := requireString(w, ro, "component")
		if !ok {
			return
		}
		st, ok := requireString(w, ro, "state")
		if !ok {
			return
		}
		if _, knownComp := componentRepos[comp]; !knownComp {
			writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
			return
		}
		if !knownHealthStates[st] {
			writeRefusal(w, http.StatusUnprocessableEntity, "E_NEEDS_SUBTOOL", "unknown_value", "this health state is not known")
			return
		}
		if seen[comp] {
			writeRefusal(w, http.StatusConflict, "E_PARSE", "duplicate", "the same component is reported twice in one batch")
			return
		}
		seen[comp] = true

		var code string
		if rawCode, hasCode := ro["code"]; hasCode {
			if err := json.Unmarshal(rawCode, &code); err != nil || !knownErrorCodes[code] {
				writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
				return
			}
		}
		var digest string
		if rawDigest, hasDigest := ro["digest"]; hasDigest {
			if err := json.Unmarshal(rawDigest, &digest); err != nil || !digestPattern.MatchString(digest) {
				writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
				return
			}
		}
		parsed = append(parsed, parsedReport{component: comp, state: st, code: code, digest: digest})
	}

	if !s.checkStoreConditions(w) {
		return
	}

	replayed := 0
	live := 0
	for _, pr := range parsed {
		prev, hadPrev := s.health[pr.component]
		prevDigest := prev.digest
		newDigest := prevDigest
		if pr.digest != "" {
			newDigest = pr.digest
		}
		s.health[pr.component] = healthEntry{
			state:  pr.state,
			code:   pr.code,
			digest: newDigest,
		}
		if hadPrev && prevDigest != "" && pr.digest != "" && pr.digest != prevDigest {
			if workersSet[pr.component] {
				var paused []*storedItem
				for _, it := range s.items {
					if it.state == StateNeedsSubtool && it.pausedBy == pr.component {
						paused = append(paused, it)
					}
				}
				sort.Slice(paused, func(i, j int) bool { return paused[i].pausedSeq < paused[j].pausedSeq })
				for _, it := range paused {
					from := it.pausedFrom
					it.state = from
					it.pausedFrom = ""
					it.pausedBy = ""
					s.seq++
					s.steps = append(s.steps, &queueStep{
						seq:        s.seq,
						kind:       KindItem,
						subjectID:  it.id,
						worker:     pr.component,
						lane:       LaneUrgent,
						readyAt:    s.now,
						lostTokens: map[string]bool{},
					})
					replayed++
				}
			}
			repo := componentRepos[pr.component]
			for _, prop := range s.proposals {
				if prop.status == "building" && prop.proposal.Repository == repo {
					prop.status = "live"
					live++
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, struct {
		Received int `json:"received"`
		Replayed int `json:"replayed"`
		Live     int `json:"live"`
	}{
		Received: len(parsed),
		Replayed: replayed,
		Live:     live,
	})
}

func (s *StandIn) handleEdits(w http.ResponseWriter, fields map[string]json.RawMessage) {
	if !checkAllowedFields(w, fields, "contract", "target", "change", "reason", "pow", "honeypot", "source") || !checkContract(w, fields) {
		return
	}
	target, ok := requireString(w, fields, "target")
	if !ok {
		return
	}
	change, ok := requireString(w, fields, "change")
	if !ok {
		return
	}
	reason, ok := requireString(w, fields, "reason")
	if !ok {
		return
	}
	pow, ok := requireString(w, fields, "pow")
	if !ok {
		return
	}
	rawHoneypot, hasHoneypot := fields["honeypot"]
	if !hasHoneypot {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "missing_field", "a required field is missing")
		return
	}
	var honeypot string
	if err := json.Unmarshal(rawHoneypot, &honeypot); err != nil {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "a field does not have the form this call requires")
		return
	}
	visitor, ok := requireString(w, fields, "source")
	if !ok {
		return
	}

	if !s.verifyPoW(pow, target, change, reason) {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "the proof of work is not valid")
		return
	}
	if honeypot != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.allowDailySubmission(visitor) {
		writeRefusal(w, http.StatusTooManyRequests, "E_TIMEOUT", "rate_limited", "too many submissions from this visitor today; try again tomorrow")
		return
	}
	own, exists := s.ownership[target]
	if !exists {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "the target is not in the ownership map")
		return
	}
	cleanChange := stripMarkup(change)
	cleanReason := stripMarkup(reason)
	if strings.TrimSpace(cleanChange) == "" {
		writeRefusal(w, http.StatusBadRequest, "E_PARSE", "bad_field", "nothing is left of the text once markup is removed")
		return
	}
	if !s.checkStoreConditions(w) {
		return
	}

	s.nextPropNum++
	id := fmt.Sprintf("EP-%04d", s.nextPropNum)
	s.proposals[id] = &storedProposal{
		id:     id,
		status: "pending_approval",
		proposal: Proposal{
			Target:     target,
			Repository: own.Repository,
			File:       own.File,
			EditKind:   own.Kind,
			Change:     cleanChange,
			Reason:     cleanReason,
		},
		results: map[string]json.RawMessage{},
	}
	s.seq++
	s.steps = append(s.steps, &queueStep{
		seq:        s.seq,
		kind:       KindProposal,
		subjectID:  id,
		worker:     "countersign",
		lane:       LaneNormal,
		readyAt:    s.now,
		lostTokens: map[string]bool{},
	})

	writeJSON(w, http.StatusAccepted, struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}{
		ID:     id,
		Status: "pending_approval",
	})
}

func (s *StandIn) handleFeed(w http.ResponseWriter) {
	if !s.checkStoreConditions(w) {
		return
	}
	at := s.now.UTC().Format(time.RFC3339)
	urgent, normal, backfill := 0, 0, 0
	for _, st := range s.steps {
		if st.held || s.now.Before(st.readyAt) {
			continue
		}
		if st.leaseToken != "" && s.now.Before(st.expiresAt) {
			continue
		}
		switch st.lane {
		case LaneUrgent:
			urgent++
		case LaneNormal:
			normal++
		case LaneBackfill:
			backfill++
		}
	}
	paused := 0
	for _, it := range s.items {
		if it.state == StateNeedsSubtool {
			paused++
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	var buf bytes.Buffer
	eventID := 1
	depthsJSON := mustJSON(struct {
		At           string `json:"at"`
		Urgent       int    `json:"urgent"`
		Normal       int    `json:"normal"`
		Backfill     int    `json:"backfill"`
		NeedsSubtool int    `json:"needs_subtool"`
	}{
		At:           at,
		Urgent:       urgent,
		Normal:       normal,
		Backfill:     backfill,
		NeedsSubtool: paused,
	})
	fmt.Fprintf(&buf, "id: %d\nevent: depths\ndata: %s\n\n", eventID, depthsJSON)

	var comps []string
	for comp := range s.health {
		comps = append(comps, comp)
	}
	sort.Strings(comps)
	for _, comp := range comps {
		eventID++
		he := s.health[comp]
		hJSON := mustJSON(struct {
			At        string `json:"at"`
			Component string `json:"component"`
			State     string `json:"state"`
			Code      string `json:"code,omitempty"`
		}{
			At:        at,
			Component: comp,
			State:     he.state,
			Code:      he.code,
		})
		fmt.Fprintf(&buf, "id: %d\nevent: health\ndata: %s\n\n", eventID, hJSON)
	}
	_, _ = w.Write(buf.Bytes())
}

func (s *StandIn) verifyPoW(token, f1, f2, f3 string) bool {
	day, nonce, ok := strings.Cut(token, ".")
	if !ok || !powNoncePattern.MatchString(nonce) {
		return false
	}
	today := s.now.UTC().Format("2006-01-02")
	yesterday := s.now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if day != today && day != yesterday {
		return false
	}
	h := sha256.New()
	for i, part := range []string{"muster-pow-v1", day, f1, f2, f3, nonce} {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	return sum[0] == 0 && sum[1] == 0 && (sum[2]&0xf0) == 0
}

func (s *StandIn) allowDailySubmission(visitor string) bool {
	day := s.now.UTC().Format("2006-01-02")
	dayMac := hmac.New(sha256.New, []byte("standin-cluster-secret"))
	dayMac.Write([]byte(day))
	salt := dayMac.Sum(nil)

	visMac := hmac.New(sha256.New, salt)
	visMac.Write([]byte(visitor))
	key := day + ":" + hex.EncodeToString(visMac.Sum(nil))

	s.dailyCounts[key]++
	return s.dailyCounts[key] <= 5
}

func stripMarkup(in string) string {
	s := in
	for _, tag := range []string{"script", "style"} {
		for {
			lower := strings.ToLower(s)
			openIdx := strings.Index(lower, "<"+tag)
			if openIdx < 0 {
				break
			}
			closeTag := "</" + tag + ">"
			closeIdx := strings.Index(lower[openIdx:], closeTag)
			if closeIdx < 0 {
				s = s[:openIdx]
				break
			}
			s = s[:openIdx] + s[openIdx+closeIdx+len(closeTag):]
		}
	}
	for {
		start := strings.Index(s, "<!--")
		if start < 0 {
			break
		}
		end := strings.Index(s[start+4:], "-->")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[start+4+end+3:]
	}
	var b strings.Builder
	inTag := false
	for i := 0; i < len(s); i++ {
		if s[i] == '<' {
			inTag = true
			continue
		}
		if s[i] == '>' && inTag {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func mustJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	writeRawJSON(w, status, mustJSON(v))
}

func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeRefusal(w http.ResponseWriter, status int, code, cause, reason string) {
	writeJSON(w, status, refusalBody{
		Error:  code,
		Cause:  cause,
		Reason: reason,
	})
}
