// Package compile turns one approved record into a credit note and a build
// specification, or into one of the public contract errors.
package compile

import (
	"encoding/json"
	"strings"

	"github.com/ClearedStack/waybill/internal/buildspec"
	"github.com/ClearedStack/waybill/internal/credit"
	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
	specprofiles "github.com/ClearedStack/waybill/tools/spec-profiles"
)

// Outbound states in Muster's contract/v1/CONTRACT.md routing table for Waybill:
//   - approved -> approved (handed to Moldloft)
//   - building -> building (handed to Moldloft on rebuilds)
//   - needs_subtool when no rule covers the item
const (
	StateApproved     = "approved"
	StateBuilding     = "building"
	StateNeedsSubtool = "needs_subtool"
)

// Result is what happened to one record in standalone compilation.
type Result struct {
	ItemID     string                  `json:"item_id,omitempty"`
	State      string                  `json:"state,omitempty"`
	CreditNote string                  `json:"credit_note,omitempty"`
	BuildSpec  *buildspec.BuildSpec    `json:"build_spec,omitempty"`
	Spec       *buildspec.MoldloftSpec `json:"spec,omitempty"`
	SLA        *record.SLA             `json:"sla,omitempty"`
	Error      *fault.Error            `json:"error,omitempty"`
}

// Compile is a pure function of data: no clock, no network, no files.
func Compile(data []byte) Result {
	rec, err := record.Parse(data)
	if rec == nil {
		return Result{Error: err}
	}
	if err == nil {
		res, _, buildErr := CompileRecord(rec)
		if buildErr == nil {
			return res
		}
		err = buildErr
	}
	res := Result{ItemID: rec.ItemID, Error: err}
	if err.Code == fault.NeedsSubtool {
		res.State = StateNeedsSubtool
	}
	return res
}

// CompileRecord compiles an already parsed Record into both the standalone
// Result and the results.waybill envelope written to Muster on POST /v1/complete.
func CompileRecord(rec *record.Record) (Result, *buildspec.WaybillResult, *fault.Error) {
	// Select the profile before rendering anything so an item with no profile
	// pauses cleanly without emitting a credit note.
	profile, err := specprofiles.Select(rec)
	if err != nil {
		return Result{}, nil, err
	}

	sla, err := rec.ComputeSLA()
	if err != nil {
		return Result{}, nil, err
	}

	optOut := rec.OptOutActive()
	note := credit.Note{
		Product:  rec.Source.Product,
		Vendor:   rec.Source.Vendor,
		Neutral:  rec.NeutralPhrase(),
		OptOut:   optOut,
		ToolType: rec.ToolType,
		Title:    rec.Title,
	}
	spec := &buildspec.BuildSpec{}
	var mCitations []buildspec.MoldloftCitation
	for _, a := range rec.Authority {
		note.Citations = append(note.Citations, a.Citation)
		spec.Authority = append(spec.Authority, buildspec.Authority{
			ID:       a.ID,
			Source:   a.Source,
			Citation: a.Citation,
			Title:    a.Title,
			URL:      a.URL,
		})
		mCitations = append(mCitations, buildspec.MoldloftCitation{
			ID:    a.ID,
			Label: a.Citation,
			Note:  a.Title,
			URL:   a.URL,
		})
	}
	text, lines, renderErr := credit.Render(note)
	if renderErr != nil {
		return Result{}, nil, fault.Broken("source", "the credit note could not be rendered as three lines")
	}
	spec.Provenance = buildspec.Provenance{
		ItemID:               rec.ItemID,
		IdeaSeenIn:           lines.IdeaSeenIn,
		WhatWeTook:           lines.WhatWeTook,
		WhatWeDidDifferently: lines.WhatWeDidDifferently,
	}

	compiled, err := profile.Build(rec)
	if err != nil {
		return Result{}, nil, err
	}
	spec.Schema = compiled.Schema
	spec.Vectors = compiled.Vectors

	slug := rec.EffectiveSlug()
	summary := rec.EffectiveSummary()
	scopeLimits := rec.EffectiveScopeLimits()

	mSpec := &buildspec.MoldloftSpec{
		ContractVersion: record.ContractVersion,
		ItemID:          rec.ItemID,
		Tool: buildspec.MoldloftTool{
			Slug:    slug,
			Title:   rec.Title,
			Summary: summary,
		},
		Pattern:        rec.ToolType,
		Citations:      mCitations,
		ScopeLimits:    scopeLimits,
		Body:           compiled.MoldloftBody,
		WorkedExamples: compiled.MoldloftExamples,
	}

	var catProv *buildspec.CatalogProvenance
	if links := rec.AuthorityLinks(); len(links) > 0 {
		catProv = &buildspec.CatalogProvenance{
			Note:  strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", " | "),
			Links: links,
		}
	}

	wbResult := &buildspec.WaybillResult{
		Slug:       slug,
		Entry:      buildspec.CatalogEntry{Title: rec.Title, Summary: summary},
		Provenance: catProv,
		BuildSpec:  spec,
		Spec:       mSpec,
		SLA:        sla,
	}

	if err := checkWall(rec, text, spec, mSpec, wbResult); err != nil {
		return Result{}, nil, err
	}

	res := Result{
		ItemID:     rec.ItemID,
		State:      rec.State,
		CreditNote: text,
		BuildSpec:  spec,
		Spec:       mSpec,
		SLA:        sla,
	}
	return res, wbResult, nil
}

// checkWall is a backstop behind the field checks. Outside the provenance
// block, no key or string may contain the product or vendor name, markup, or
// anything that looks like a link (except checked public https URLs in
// Authority.URL / Citations.URL). When an opt-out is active, even the credit
// note and provenance block must not contain the product or vendor name. No
// forbidden raw observation key may appear anywhere in the output.
func checkWall(rec *record.Record, creditText string, spec *buildspec.BuildSpec, mSpec *buildspec.MoldloftSpec, wb *buildspec.WaybillResult) *fault.Error {
	fullRaw, err := json.Marshal(wb)
	if err != nil {
		return fault.Broken("build_spec", "the build specification could not be encoded")
	}
	if buildspec.HasForbiddenObservationKey(fullRaw) {
		return fault.Broken("build_spec", "the build specification contains a forbidden observation key")
	}

	names := []string{record.Fold(rec.Source.Product), record.Fold(rec.Source.Vendor)}

	// Strip only checked Authority/Citation URLs before checking the rest of the
	// non-provenance structures for any links, markup, or vendor names.
	authNoURL := make([]buildspec.Authority, len(spec.Authority))
	for i, a := range spec.Authority {
		if a.URL != "" {
			if !record.PublicHTTPSURL(a.URL) || containsAnyFolded(a.URL, names) {
				return fault.Broken("build_spec", "an authority url failed the clean-room wall check")
			}
		}
		a.URL = ""
		authNoURL[i] = a
	}
	citNoURL := make([]buildspec.MoldloftCitation, len(mSpec.Citations))
	for i, c := range mSpec.Citations {
		if c.URL != "" {
			if !record.PublicHTTPSURL(c.URL) || containsAnyFolded(c.URL, names) {
				return fault.Broken("build_spec", "a citation url failed the clean-room wall check")
			}
		}
		c.URL = ""
		citNoURL[i] = c
	}

	nonProvenance := []any{
		authNoURL,
		spec.Schema,
		spec.Vectors,
		mSpec.Tool,
		mSpec.ScopeLimits,
		citNoURL,
		mSpec.Body,
		mSpec.WorkedExamples,
		wb.Slug,
		wb.Entry,
	}
	raw, err := json.Marshal(nonProvenance)
	if err != nil {
		return fault.Broken("build_spec", "the build specification could not be encoded")
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return fault.Broken("build_spec", "the build specification could not be encoded")
	}
	if !clean(tree, names) {
		return fault.Broken("build_spec", "vendor material was found outside the provenance block")
	}

	if rec.OptOutActive() {
		if containsAnyFolded(creditText, names) ||
			containsAnyFolded(spec.Provenance.IdeaSeenIn, names) ||
			containsAnyFolded(spec.Provenance.WhatWeTook, names) ||
			containsAnyFolded(spec.Provenance.WhatWeDidDifferently, names) {
			return fault.Broken("build_spec", "vendor or product name leaked despite active opt-out")
		}
		if wb.Provenance != nil && containsAnyFolded(wb.Provenance.Note, names) {
			return fault.Broken("build_spec", "vendor or product name leaked in catalog provenance despite active opt-out")
		}
	}
	return nil
}

func containsAnyFolded(s string, names []string) bool {
	folded := record.Fold(s)
	for _, name := range names {
		if name != "" && strings.Contains(folded, name) {
			return true
		}
	}
	return false
}

func clean(v any, names []string) bool {
	switch v := v.(type) {
	case string:
		if strings.ContainsAny(v, "<>") || strings.Contains(v, "://") || strings.Contains(strings.ToLower(v), "www.") {
			return false
		}
		if containsAnyFolded(v, names) {
			return false
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
