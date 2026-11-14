package muster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/waybill/internal/fault"
)

const examplesDir = "../../testdata/contract/v1/examples"

type contractExample struct {
	About   string       `json:"about"`
	Given   exampleGiven `json:"given"`
	Request exampleCall  `json:"request"`
	Expect  struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
		Stream string          `json:"stream"`
	} `json:"expect"`
}

type exampleGiven struct {
	Items     []SeedItem    `json:"items"`
	Calls     []exampleCall `json:"calls"`
	Wait      string        `json:"wait"`
	Condition string        `json:"condition"`
}

type exampleCall struct {
	As          string          `json:"as"`
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Body        json.RawMessage `json:"body"`
	Raw         *string         `json:"raw"`
	ContentType *string         `json:"content_type"`
}

// TestContractExamples replays every saved example from
// ClearedStack/muster/contract/v1/examples/ against the StandIn server and,
// where the call is one HTTPClient makes, also replays it through HTTPClient.
func TestContractExamples(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(examplesDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 52 {
		t.Fatalf("found %d example files in %s, expected at least 52", len(files), examplesDir)
	}

	clientReplayed := 0
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var ex contractExample
			if err := json.Unmarshal(raw, &ex); err != nil {
				t.Fatalf("decode example: %v", err)
			}

			srv, standIn := setupExampleServer(t, ex.Given)
			defer srv.Close()

			status, body := doRawCall(t, srv.URL, ex.Request)
			if status != ex.Expect.Status {
				t.Fatalf("status = %d (%s), want %d", status, string(body), ex.Expect.Status)
			}
			if ex.Expect.Stream != "" {
				if string(body) != ex.Expect.Stream {
					t.Fatalf("stream mismatch:\ngot:\n%q\nwant:\n%q", string(body), ex.Expect.Stream)
				}
			} else if len(ex.Expect.Body) > 0 {
				gotCompact := compactJSON(t, body)
				wantCompact := compactJSON(t, ex.Expect.Body)
				if gotCompact != wantCompact {
					t.Fatalf("body mismatch:\ngot:  %s\nwant: %s", gotCompact, wantCompact)
				}
			} else if len(body) != 0 {
				t.Fatalf("expected empty body, got %q", string(body))
			}

			_ = standIn
			if replayWithHTTPClient(t, ex) {
				clientReplayed++
			}
		})
	}
	if clientReplayed < 20 {
		t.Fatalf("HTTPClient replayed only %d examples, expected at least 20", clientReplayed)
	}
}

func setupExampleServer(t *testing.T, g exampleGiven) (*httptest.Server, *StandIn) {
	t.Helper()
	standIn := NewStandIn()
	if len(g.Items) > 0 {
		standIn.Seed(g.Items)
	}
	srv := httptest.NewServer(standIn)
	for i, c := range g.Calls {
		st, b := doRawCall(t, srv.URL, c)
		if st < 200 || st >= 300 {
			srv.Close()
			t.Fatalf("given.calls[%d] (%s %s) failed: %d %s", i, c.Method, c.Path, st, string(b))
		}
	}
	if g.Wait != "" {
		d, err := time.ParseDuration(g.Wait)
		if err != nil {
			srv.Close()
			t.Fatalf("parse wait %q: %v", g.Wait, err)
		}
		standIn.Advance(d)
	}
	if g.Condition != "" {
		standIn.SetCondition(g.Condition)
	}
	return srv, standIn
}

func doRawCall(t *testing.T, baseURL string, c exampleCall) (int, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if c.Raw != nil {
		bodyReader = strings.NewReader(*c.Raw)
	} else if len(c.Body) > 0 {
		bodyReader = bytes.NewReader(c.Body)
	}
	req, err := http.NewRequest(c.Method, baseURL+c.Path, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if c.Method == http.MethodPost {
		if c.ContentType != nil {
			req.Header.Set("Content-Type", *c.ContentType)
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if c.As != "" {
		req.Header.Set("Authorization", "Bearer "+c.As)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact JSON %q: %v", string(raw), err)
	}
	return buf.String()
}

// replayWithHTTPClient runs examples whose request matches HTTPClient's wire
// calls through HTTPClient against a fresh StandIn, checking parsed results and
// refusal classifications.
func replayWithHTTPClient(t *testing.T, ex contractExample) bool {
	t.Helper()
	req := ex.Request
	if req.Method != http.MethodPost || req.Raw != nil || req.ContentType != nil {
		return false
	}

	var bodyMap map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &bodyMap); err != nil {
		return false
	}
	var contractStr string
	if rawC, ok := bodyMap["contract"]; !ok || json.Unmarshal(rawC, &contractStr) != nil || contractStr != ContractVersion {
		return false
	}

	checkExtra := func(allowed ...string) bool {
		set := map[string]bool{}
		for _, k := range allowed {
			set[k] = true
		}
		for k := range bodyMap {
			if !set[k] {
				return false
			}
		}
		return true
	}

	srv, _ := setupExampleServer(t, ex.Given)
	defer srv.Close()

	cli, err := NewHTTPClient(HTTPConfig{
		BaseURL: srv.URL,
		Token:   req.As,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var callErr error
	switch req.Path {
	case "/v1/lease":
		if !checkExtra("contract") {
			return false
		}
		var g *Grant
		g, callErr = cli.Lease(ctx)
		if callErr == nil {
			if ex.Expect.Status == http.StatusNoContent {
				if g != nil {
					t.Fatalf("expected nil grant on 204, got %+v", g)
				}
			} else if g == nil {
				t.Fatalf("expected grant on 200")
			}
		}

	case "/v1/heartbeat":
		if !checkExtra("contract", "lease") {
			return false
		}
		var lease string
		if rawL, ok := bodyMap["lease"]; ok && json.Unmarshal(rawL, &lease) != nil {
			return false
		}
		_, callErr = cli.Heartbeat(ctx, lease)

	case "/v1/complete":
		if !checkExtra("contract", "lease", "state", "result") {
			return false
		}
		var lease, state string
		if rawL, ok := bodyMap["lease"]; ok && json.Unmarshal(rawL, &lease) != nil {
			return false
		}
		if rawS, ok := bodyMap["state"]; ok && json.Unmarshal(rawS, &state) != nil {
			return false
		}
		_, callErr = cli.Complete(ctx, lease, state, bodyMap["result"])

	case "/v1/fail":
		if !checkExtra("contract", "lease", "code") {
			return false
		}
		var lease, code string
		if rawL, ok := bodyMap["lease"]; ok && json.Unmarshal(rawL, &lease) != nil {
			return false
		}
		if rawCode, ok := bodyMap["code"]; ok && json.Unmarshal(rawCode, &code) != nil {
			return false
		}
		_, callErr = cli.Fail(ctx, lease, fault.Code(code))

	case "/v1/needs_subtool":
		if !checkExtra("contract", "lease") {
			return false
		}
		var lease string
		if rawL, ok := bodyMap["lease"]; ok && json.Unmarshal(rawL, &lease) != nil {
			return false
		}
		_, callErr = cli.NeedsSubtool(ctx, lease)

	case "/v1/release":
		if !checkExtra("contract", "id") {
			return false
		}
		var id string
		if rawID, ok := bodyMap["id"]; ok && json.Unmarshal(rawID, &id) != nil {
			return false
		}
		_, callErr = cli.Release(ctx, id)

	default:
		return false
	}

	if ex.Expect.Status >= 200 && ex.Expect.Status < 300 {
		if callErr != nil {
			t.Fatalf("HTTPClient unexpected error: %v", callErr)
		}
		return true
	}

	if callErr == nil {
		t.Fatalf("HTTPClient expected refusal %d, got nil", ex.Expect.Status)
	}
	var ce *CallError
	if !errors.As(callErr, &ce) {
		t.Fatalf("expected *CallError, got %T (%v)", callErr, callErr)
	}
	var wantRef refusalBody
	if err := json.Unmarshal(ex.Expect.Body, &wantRef); err != nil {
		t.Fatal(err)
	}
	if ce.Status != ex.Expect.Status || string(ce.Code) != wantRef.Error || ce.Cause != wantRef.Cause {
		t.Fatalf("CallError = %+v, want status=%d code=%s cause=%s", ce, ex.Expect.Status, wantRef.Error, wantRef.Cause)
	}
	if strings.Contains(ce.Error(), "127.0.0.1") || strings.Contains(ce.Error(), "http") {
		t.Fatalf("CallError leaked address: %q", ce.Error())
	}
	return true
}

func TestWaybillCompleteAllowsUpTo256KiB(t *testing.T) {
	standIn := NewStandIn()
	standIn.Seed([]SeedItem{{
		ID:    "PA-0001",
		State: StateApproved,
		Lane:  LaneNormal,
		Next:  "waybill",
	}})
	srv := httptest.NewServer(standIn)
	defer srv.Close()

	tokFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokFile, []byte("waybill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli, err := NewHTTPClient(HTTPConfig{
		BaseURL:   srv.URL,
		TokenFile: tokFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	grant, err := cli.Lease(ctx)
	if err != nil || grant == nil {
		t.Fatalf("lease: %v, %+v", err, grant)
	}

	// 100 KiB payload: above 64 KiB general limit, well inside Waybill's 256 KiB exception.
	largePayload := json.RawMessage(`{"spec":"` + strings.Repeat("a", 100<<10) + `"}`)
	ack, err := cli.Complete(ctx, grant.Lease, StateApproved, largePayload)
	if err != nil {
		t.Fatalf("100 KiB Waybill complete failed: %v", err)
	}
	if ack.ID != "PA-0001" || ack.State != StateApproved || ack.Next != "moldloft" {
		t.Fatalf("unexpected complete ack: %+v", ack)
	}

	// Idempotent repeat of the call that ended the lease returns the same ack.
	ack2, err := cli.Complete(ctx, grant.Lease, StateApproved, largePayload)
	if err != nil || *ack2 != *ack {
		t.Fatalf("idempotent repeat mismatch: %+v, err=%v", ack2, err)
	}

	// Payload exceeding 256 KiB is rejected before hitting the wire.
	tooBig := json.RawMessage(`{"spec":"` + strings.Repeat("a", MaxWaybillCompleteBytes) + `"}`)
	if _, err := cli.Complete(ctx, grant.Lease, StateApproved, tooBig); err == nil || CodeOf(err) != fault.Parse {
		t.Fatalf("expected E_PARSE for >256 KiB payload, got %v", err)
	}
}
