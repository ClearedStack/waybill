// Package compile turns one approved record into a credit note and a build
// specification, or into one of the three public errors.
package compile

import (
	"encoding/json"
	"strings"

	"github.com/clearedstack/waybill/internal/buildspec"
	"github.com/clearedstack/waybill/internal/credit"
	"github.com/clearedstack/waybill/internal/fault"
	"github.com/clearedstack/waybill/internal/record"
	specprofiles "github.com/clearedstack/waybill/tools/spec-profiles"
)

// Outbound states.
const (
	StateBuilding     = "building"
	StateNeedsSubtool = "needs_subtool"
)

// Result is what happened to one record.
type Result struct {
	ItemID     string               `json:"item_id,omitempty"`
	State      string               `json:"state,omitempty"`
	CreditNote string               `json:"credit_note,omitempty"`
	BuildSpec  *buildspec.BuildSpec `json:"build_spec,omitempty"`
	Error      *fault.Error         `json:"error,omitempty"`
}

// Compile is a pure function of data: no clock, no network, no files.
func Compile(data []byte) Result {
	rec, err := record.Parse(data)
	if rec == nil {
		return Result{Error: err}
	}
	if err == nil {
		var res Result
		if res, err = build(rec); err == nil {
			return res
		}
	}
	res := Result{ItemID: rec.ItemID, Error: err}
	if err.Code == fault.NeedsSubtool {
		res.State = StateNeedsSubtool
	}
	return res
}

func build(rec *record.Record) (Result, *fault.Error) {
	// The profile is chosen before anything is rendered, so an item with no
	// profile is paused without a credit note being produced for it.
	profile, err := specprofiles.Select(rec)
	if err != nil {
		return Result{}, err
	}

	note := credit.Note{
		Product:  rec.Source.Product,
		Vendor:   rec.Source.Vendor,
		ToolType: rec.ToolType,
		Title:    rec.Title,
	}
	spec := &buildspec.BuildSpec{}
	for _, a := range rec.Authority {
		note.Citations = append(note.Citations, a.Citation)
		spec.Authority = append(spec.Authority, buildspec.Authority{ID: a.ID, Source: a.Source, Citation: a.Citation, Title: a.Title})
	}
	text, lines, renderErr := credit.Render(note)
	if renderErr != nil {
		return Result{}, fault.Broken("source", "the credit note could not be rendered as three lines")
	}
	spec.Provenance = buildspec.Provenance{
		ItemID:               rec.ItemID,
		IdeaSeenIn:           lines.IdeaSeenIn,
		WhatWeTook:           lines.WhatWeTook,
		WhatWeDidDifferently: lines.WhatWeDidDifferently,
	}

	if spec.Schema, spec.Vectors, err = profile.Build(rec); err != nil {
		return Result{}, err
	}
	if err := checkWall(rec, spec); err != nil {
		return Result{}, err
	}
	return Result{ItemID: rec.ItemID, State: StateBuilding, CreditNote: text, BuildSpec: spec}, nil
}

// checkWall is a last check on the finished specification. Outside the
// provenance block, no key or string may contain the product or vendor name,
// markup, or anything that looks like a link.
//
// It is a backstop. The main protection is that a specification is assembled
// field by field from checked values, and vendor material is never read.
func checkWall(rec *record.Record, spec *buildspec.BuildSpec) *fault.Error {
	raw, err := json.Marshal([]any{spec.Authority, spec.Schema, spec.Vectors})
	if err != nil {
		return fault.Broken("build_spec", "the build specification could not be encoded")
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return fault.Broken("build_spec", "the build specification could not be encoded")
	}
	names := []string{record.Fold(rec.Source.Product), record.Fold(rec.Source.Vendor)}
	if !clean(tree, names) {
		return fault.Broken("build_spec", "vendor material was found outside the provenance block")
	}
	return nil
}

func clean(v any, names []string) bool {
	switch v := v.(type) {
	case string:
		if strings.ContainsAny(v, "<>") || strings.Contains(v, "://") || strings.Contains(strings.ToLower(v), "www.") {
			return false
		}
		folded := record.Fold(v)
		for _, name := range names {
			if strings.Contains(folded, name) {
				return false
			}
		}
	case []any:
		for _, item := range v {
			if !clean(item, names) {
				return false
			}
		}
	case map[string]any:
		for key, item := range v {
			if !clean(key, names) || !clean(item, names) {
				return false
			}
		}
	}
	return true
}
