// Package buildspec defines the sanitized JSON build specification: the one
// thing Waybill lets through the clean-room wall.
package buildspec

import (
	"bytes"
	"encoding/json"
)

// BuildSpec has exactly the four parts SPEC.md names.
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
