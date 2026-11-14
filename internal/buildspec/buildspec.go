// Package buildspec defines the sanitized JSON build specification and the
// results.waybill envelope Waybill emits across the clean-room wall.
package buildspec

import (
	"bytes"
	"encoding/json"

	"github.com/ClearedStack/waybill/internal/record"
)

// BuildSpec has the four parts Waybill's SPEC.md names.
type BuildSpec struct {
	Provenance Provenance  `json:"provenance"`
	Authority  []Authority `json:"authority"`
	// Schema and Vectors are written by the spec-profile for the tool type.
	Schema  any `json:"schema"`
	Vectors any `json:"vectors"`
}

// Provenance is the credit note in structured form.
type Provenance struct {
	ItemID               string `json:"item_id"`
	IdeaSeenIn           string `json:"idea_seen_in"`
	WhatWeTook           string `json:"what_we_took"`
	WhatWeDidDifferently string `json:"what_we_did_differently"`
}

// Authority is one official publication the tool is built from.
type Authority struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Citation string `json:"citation"`
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
}

// MoldloftTool holds the slug, title, and summary inside MoldloftSpec.
type MoldloftTool struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// MoldloftCitation is one citation entry inside MoldloftSpec.
type MoldloftCitation struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
	URL   string `json:"url,omitempty"`
}

// MoldloftSpec is the build-spec-v1 structure Moldloft extracts from
// results.waybill.spec when generating a static tool repository.
type MoldloftSpec struct {
	ContractVersion string             `json:"contract_version"`
	ItemID          string             `json:"item_id"`
	Tool            MoldloftTool       `json:"tool"`
	Pattern         string             `json:"pattern"`
	Citations       []MoldloftCitation `json:"citations"`
	ScopeLimits     []string           `json:"scope_limits"`
	Body            any                `json:"body"`
	WorkedExamples  any                `json:"worked_examples"`
}

// CatalogEntry is the catalog title and summary block read by Chartroom.
type CatalogEntry struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// CatalogProvenance is the two-field provenance block read by Chartroom and
// Tidegauge.
type CatalogProvenance struct {
	Note  string   `json:"note"`
	Links []string `json:"links"`
}

// WaybillResult is the JSON object Waybill records in Muster under
// results.waybill on POST /v1/complete.
type WaybillResult struct {
	Slug       string             `json:"slug"`
	Entry      CatalogEntry       `json:"entry"`
	Provenance *CatalogProvenance `json:"provenance,omitempty"`
	BuildSpec  *BuildSpec         `json:"build_spec"`
	Spec       *MoldloftSpec      `json:"spec"`
	SLA        *record.SLA        `json:"sla,omitempty"`
}

var forbiddenObservationKeys = map[string]bool{
	"html":             true,
	"raw_html":         true,
	"raw_text":         true,
	"body_text":        true,
	"page_text":        true,
	"dom":              true,
	"headers":          true,
	"response_headers": true,
	"screenshot":       true,
	"screenshots":      true,
	"snapshot":         true,
	"snapshots":        true,
	"observation":      true,
	"observations":     true,
}

// HasForbiddenObservationKey reports whether raw JSON contains any field name
// reserved for raw upstream observation data.
func HasForbiddenObservationKey(raw []byte) bool {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return true
	}
	return walkForbidden(tree)
}

func walkForbidden(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if forbiddenObservationKeys[k] || walkForbidden(child) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if walkForbidden(item) {
				return true
			}
		}
	}
	return false
}

// Encode writes v as indented JSON ending in a newline. Field order is the
// order of the struct fields and map keys are sorted, so equal values always
// give equal bytes.
func Encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// EncodeCompact writes v as compact single-line JSON with no HTML escaping and
// no trailing newline.
func EncodeCompact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
