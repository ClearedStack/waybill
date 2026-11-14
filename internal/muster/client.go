// Package muster implements Waybill's client and in-memory stand-in for
// Muster's contract v1 (ClearedStack/muster/contract/v1/CONTRACT.md).
package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

// ContractVersion is the only Muster contract version Waybill speaks.
const ContractVersion = "v1"

// DefaultTokenFile is the projected Kubernetes ServiceAccount token path.
const DefaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// Request and response size bounds from CONTRACT.md.
const (
	MaxWorkerRequestBytes   = 64 << 10
	MaxWaybillCompleteBytes = 256 << 10
	MaxResponseBytes        = 256 << 10
)

// Step kinds in CONTRACT.md.
const (
	KindItem     = "item"
	KindProposal = "proposal"
	KindIncident = "incident"
)

// Priority lanes in CONTRACT.md, in drain order.
const (
	LaneUrgent   = "urgent"
	LaneNormal   = "normal"
	LaneBackfill = "backfill"
)

// Item states relevant to Waybill.
const (
	StateApproved     = "approved"
	StateBuilding     = "building"
	StateNeedsSubtool = "needs_subtool"
)

// Client is the Muster contract v1 surface Waybill calls.
type Client interface {
	Lease(ctx context.Context) (*Grant, error)
	Heartbeat(ctx context.Context, lease string) (*HeartbeatAck, error)
	Complete(ctx context.Context, lease, state string, result json.RawMessage) (*CompleteAck, error)
	Fail(ctx context.Context, lease string, code fault.Code) (*FailAck, error)
	NeedsSubtool(ctx context.Context, lease string) (*NeedsSubtoolAck, error)
}

// Found holds the discovery metadata Muster attaches to an item recorded via
// POST /v1/items.
type Found struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	Address string `json:"address"`
}

// Proposal holds an edit proposal payload on a proposal lease.
type Proposal struct {
	Target     string `json:"target"`
	Repository string `json:"repository"`
	File       string `json:"file"`
	EditKind   string `json:"edit_kind"`
	Change     string `json:"change"`
	Reason     string `json:"reason"`
}

// Incident holds an incident payload on a Bellbuoy incident lease.
type Incident struct {
	ID     string `json:"id"`
	Worker string `json:"worker"`
	Code   string `json:"code"`
	State  string `json:"state"`
}

// Grant is Muster's 200 answer to POST /v1/lease.
type Grant struct {
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

// HeartbeatAck is Muster's 200 answer to POST /v1/heartbeat.
type HeartbeatAck struct {
	Lease   string `json:"lease"`
	Expires string `json:"expires"`
}

// CompleteAck is Muster's 200 answer to POST /v1/complete.
type CompleteAck struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Next  string `json:"next,omitempty"`
}

// FailAck is Muster's 200 answer to POST /v1/fail.
type FailAck struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Next  string `json:"next,omitempty"`
	Held  bool   `json:"held,omitempty"`
}

// NeedsSubtoolAck is Muster's 200 answer to POST /v1/needs_subtool.
type NeedsSubtoolAck struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// ReleaseAck is Muster's 200 answer to POST /v1/release.
type ReleaseAck struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Next  string `json:"next"`
}

// knownCauses is the closed set of refusal causes defined in CONTRACT.md.
var knownCauses = map[string]bool{
	"not_json":         true,
	"unknown_field":    true,
	"missing_field":    true,
	"bad_field":        true,
	"no_credential":    true,
	"not_your_call":    true,
	"no_such_call":     true,
	"wrong_method":     true,
	"too_large":        true,
	"wrong_type":       true,
	"lease_not_held":   true,
	"move_not_allowed": true,
	"duplicate":        true,
	"unknown_contract": true,
	"unknown_value":    true,
	"rate_limited":     true,
	"store_slow":       true,
	"busy":             true,
	"bad_record":       true,
	"unexpected":       true,
}

// CallError is a sanitized error returned by an HTTP call to Muster. It never
// carries hostnames, URLs, tokens, or raw upstream text.
type CallError struct {
	Code      fault.Code
	Status    int
	Cause     string
	Retryable bool
}

func (e *CallError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != "" {
		return fmt.Sprintf("%s: muster refused call (%d %s)", e.Code, e.Status, e.Cause)
	}
	if e.Status > 0 {
		return fmt.Sprintf("%s: muster status %d", e.Code, e.Status)
	}
	return string(e.Code) + ": muster call failed"
}

// CodeOf extracts the public error code from err, defaulting to E_PARSE.
func CodeOf(err error) fault.Code {
	if err == nil {
		return ""
	}
	var ce *CallError
	if errors.As(err, &ce) && ce.Code != "" {
		return ce.Code
	}
	var fe *fault.Error
	if errors.As(err, &fe) && fe.Code != "" {
		return fe.Code
	}
	return fault.Parse
}

// IsRetryable reports whether a failed Muster call should be retried with
// backoff while keeping any staged outcome in /var/spool/cs.
func IsRetryable(err error) bool {
	var ce *CallError
	return errors.As(err, &ce) && ce.Retryable
}

// IsTerminalOutcomeRefusal reports whether Muster permanently refused an
// outcome for a specific lease (for example 409 lease_not_held or
// move_not_allowed, or 400 bad_field), meaning resending the same staged
// outcome can never succeed.
func IsTerminalOutcomeRefusal(err error) bool {
	var ce *CallError
	if !errors.As(err, &ce) || ce.Retryable {
		return false
	}
	switch ce.Status {
	case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge:
		return true
	case http.StatusInternalServerError:
		return ce.Cause == "bad_record"
	}
	return false
}

// HTTPConfig configures HTTPClient.
type HTTPConfig struct {
	BaseURL    string
	Contract   string
	TokenFile  string
	Token      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// HTTPClient speaks Muster contract v1 over HTTP.
type HTTPClient struct {
	baseURL   string
	contract  string
	tokenFile string
	token     string
	http      *http.Client
}

// NewHTTPClient validates cfg and constructs an HTTPClient.
func NewHTTPClient(cfg HTTPConfig) (*HTTPClient, error) {
	contract := strings.TrimSpace(cfg.Contract)
	if contract == "" {
		contract = ContractVersion
	}
	if contract != ContractVersion {
		return nil, fault.Unfamiliar("contract_version", "no rule covers this contract version")
	}
	rawURL := strings.TrimSpace(cfg.BaseURL)
	if rawURL == "" {
		return nil, fault.Broken("muster_url", "the Muster address is missing")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fault.Broken("muster_url", "the Muster address is malformed")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	return &HTTPClient{
		baseURL:   strings.TrimRight(u.String(), "/"),
		contract:  contract,
		tokenFile: cfg.TokenFile,
		token:     strings.TrimSpace(cfg.Token),
		http:      hc,
	}, nil
}

type leaseReq struct {
	Contract string `json:"contract"`
}

type heartbeatReq struct {
	Contract string `json:"contract"`
	Lease    string `json:"lease"`
}

type completeReq struct {
	Contract string          `json:"contract"`
	Lease    string          `json:"lease"`
	State    string          `json:"state"`
	Result   json.RawMessage `json:"result,omitempty"`
}

type failReq struct {
	Contract string     `json:"contract"`
	Lease    string     `json:"lease"`
	Code     fault.Code `json:"code"`
}

type needsSubtoolReq struct {
	Contract string `json:"contract"`
	Lease    string `json:"lease"`
}

type releaseReq struct {
	Contract string `json:"contract"`
	ID       string `json:"id"`
}

type refusalBody struct {
	Error  string `json:"error"`
	Cause  string `json:"cause"`
	Reason string `json:"reason"`
}

// Lease calls POST /v1/lease. It returns (nil, nil) on 204 No Content.
func (c *HTTPClient) Lease(ctx context.Context) (*Grant, error) {
	status, raw, err := c.post(ctx, "/v1/lease", leaseReq{Contract: c.contract}, MaxWorkerRequestBytes)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	var g Grant
	if err := strictjson.Decode(raw, &g); err != nil || g.Lease == "" || g.Kind == "" || g.ID == "" || g.State == "" || g.Lane == "" || g.Attempt < 1 || g.Expires == "" {
		return nil, &CallError{Code: fault.Parse, Status: status}
	}
	return &g, nil
}

// Heartbeat calls POST /v1/heartbeat.
func (c *HTTPClient) Heartbeat(ctx context.Context, lease string) (*HeartbeatAck, error) {
	status, raw, err := c.post(ctx, "/v1/heartbeat", heartbeatReq{Contract: c.contract, Lease: lease}, MaxWorkerRequestBytes)
	if err != nil {
		return nil, err
	}
	var ack HeartbeatAck
	if err := strictjson.Decode(raw, &ack); err != nil || ack.Lease == "" || ack.Expires == "" {
		return nil, &CallError{Code: fault.Parse, Status: status}
	}
	return &ack, nil
}

// Complete calls POST /v1/complete. Waybill complete payloads may be up to
// 256 KiB under CONTRACT.md.
func (c *HTTPClient) Complete(ctx context.Context, lease, state string, result json.RawMessage) (*CompleteAck, error) {
	req := completeReq{
		Contract: c.contract,
		Lease:    lease,
		State:    state,
		Result:   result,
	}
	status, raw, err := c.post(ctx, "/v1/complete", req, MaxWaybillCompleteBytes)
	if err != nil {
		return nil, err
	}
	var ack CompleteAck
	if err := strictjson.Decode(raw, &ack); err != nil || ack.ID == "" || ack.State == "" {
		return nil, &CallError{Code: fault.Parse, Status: status}
	}
	return &ack, nil
}

// Fail calls POST /v1/fail with E_PARSE or E_TIMEOUT.
func (c *HTTPClient) Fail(ctx context.Context, lease string, code fault.Code) (*FailAck, error) {
	status, raw, err := c.post(ctx, "/v1/fail", failReq{Contract: c.contract, Lease: lease, Code: code}, MaxWorkerRequestBytes)
	if err != nil {
		return nil, err
	}
	var ack FailAck
	if err := strictjson.Decode(raw, &ack); err != nil || ack.ID == "" || ack.State == "" {
		return nil, &CallError{Code: fault.Parse, Status: status}
	}
	return &ack, nil
}

// NeedsSubtool calls POST /v1/needs_subtool.
func (c *HTTPClient) NeedsSubtool(ctx context.Context, lease string) (*NeedsSubtoolAck, error) {
	status, raw, err := c.post(ctx, "/v1/needs_subtool", needsSubtoolReq{Contract: c.contract, Lease: lease}, MaxWorkerRequestBytes)
	if err != nil {
		return nil, err
	}
	var ack NeedsSubtoolAck
	if err := strictjson.Decode(raw, &ack); err != nil || ack.ID == "" || ack.State == "" {
		return nil, &CallError{Code: fault.Parse, Status: status}
	}
	return &ack, nil
}

// Release calls POST /v1/release (permitted to Countersign in CONTRACT.md;
// exposed here so contract tests can verify every worker/control endpoint).
func (c *HTTPClient) Release(ctx context.Context, id string) (*ReleaseAck, error) {
	status, raw, err := c.post(ctx, "/v1/release", releaseReq{Contract: c.contract, ID: id}, MaxWorkerRequestBytes)
	if err != nil {
		return nil, err
	}
	var ack ReleaseAck
	if err := strictjson.Decode(raw, &ack); err != nil || ack.ID == "" || ack.State == "" || ack.Next == "" {
		return nil, &CallError{Code: fault.Parse, Status: status}
	}
	return &ack, nil
}

func (c *HTTPClient) bearerToken() (string, error) {
	if c.tokenFile != "" {
		b, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return "", &CallError{Code: fault.Timeout, Retryable: true}
		}
		return strings.TrimSpace(string(b)), nil
	}
	return c.token, nil
}

func (c *HTTPClient) post(ctx context.Context, path string, payload any, limit int) (int, []byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return 0, nil, &CallError{Code: fault.Parse}
	}
	bodyBytes := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(bodyBytes) > limit {
		return 0, nil, &CallError{Code: fault.Parse, Status: http.StatusRequestEntityTooLarge, Cause: "too_large"}
	}

	tok, err := c.bearerToken()
	if err != nil {
		return 0, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, nil, &CallError{Code: fault.Parse}
	}
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, &CallError{Code: fault.Timeout, Retryable: true}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, &CallError{Code: fault.Timeout, Status: resp.StatusCode, Retryable: true}
	}
	if len(raw) > MaxResponseBytes {
		return resp.StatusCode, nil, &CallError{Code: fault.Parse, Status: resp.StatusCode}
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, raw, nil
	}
	return resp.StatusCode, nil, decodeRefusal(resp.StatusCode, raw)
}

func decodeRefusal(status int, raw []byte) *CallError {
	ce := &CallError{
		Status:    status,
		Code:      defaultCodeForStatus(status),
		Retryable: status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || status == http.StatusBadGateway || status == http.StatusGatewayTimeout,
	}
	var ref refusalBody
	if err := strictjson.Decode(raw, &ref); err == nil {
		switch fault.Code(ref.Error) {
		case fault.Timeout:
			ce.Code = fault.Timeout
			ce.Retryable = true
		case fault.NeedsSubtool:
			ce.Code = fault.NeedsSubtool
		case fault.Parse:
			ce.Code = fault.Parse
		}
		if knownCauses[ref.Cause] {
			ce.Cause = ref.Cause
		}
	}
	return ce
}

func defaultCodeForStatus(status int) fault.Code {
	switch status {
	case http.StatusUnprocessableEntity:
		return fault.NeedsSubtool
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return fault.Timeout
	default:
		return fault.Parse
	}
}
