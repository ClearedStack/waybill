// Package specprofiles is the spec-profiles sub-tool: one profile per tool
// type, each turning an approved record into the schema and vectors of a
// build specification and the pattern-specific body and worked examples that
// Moldloft consumes.
package specprofiles

import (
	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
	"github.com/ClearedStack/waybill/tools/spec-profiles/calculator"
	"github.com/ClearedStack/waybill/tools/spec-profiles/checklist"
	decisiontree "github.com/ClearedStack/waybill/tools/spec-profiles/decision-tree"
	"github.com/ClearedStack/waybill/tools/spec-profiles/validator"
)

// Compiled holds both Waybill's SPEC.md schema/vectors and Moldloft's
// build-spec-v1 body/worked_examples for one compiled record.
type Compiled struct {
	Schema           any
	Vectors          any
	MoldloftBody     any
	MoldloftExamples any
}

// Profile is one tool-type compiler.
type Profile struct {
	ToolType string
	// Matches is the matching rule.
	Matches func(*record.Record) bool
	// Build compiles and verifies the tool payload.
	Build func(*record.Record) (Compiled, *fault.Error)
}

// profiles is every profile in the binary. It must agree with
// tools/registry.yaml.
var profiles = []Profile{
	{
		ToolType: calculator.ToolType,
		Matches:  calculator.Matches,
		Build: func(rec *record.Record) (Compiled, *fault.Error) {
			s, v, mb, me, err := calculator.BuildFull(rec)
			if err != nil {
				return Compiled{}, err
			}
			return Compiled{Schema: s, Vectors: v, MoldloftBody: mb, MoldloftExamples: me}, nil
		},
	},
	{
		ToolType: checklist.ToolType,
		Matches:  checklist.Matches,
		Build: func(rec *record.Record) (Compiled, *fault.Error) {
			s, v, mb, me, err := checklist.BuildFull(rec)
			if err != nil {
				return Compiled{}, err
			}
			return Compiled{Schema: s, Vectors: v, MoldloftBody: mb, MoldloftExamples: me}, nil
		},
	},
	{
		ToolType: validator.ToolType,
		Matches:  validator.Matches,
		Build: func(rec *record.Record) (Compiled, *fault.Error) {
			s, v, mb, me, err := validator.BuildFull(rec)
			if err != nil {
				return Compiled{}, err
			}
			return Compiled{Schema: s, Vectors: v, MoldloftBody: mb, MoldloftExamples: me}, nil
		},
	},
	{
		ToolType: decisiontree.ToolType,
		Matches:  decisiontree.Matches,
		Build: func(rec *record.Record) (Compiled, *fault.Error) {
			s, v, mb, me, err := decisiontree.BuildFull(rec)
			if err != nil {
				return Compiled{}, err
			}
			return Compiled{Schema: s, Vectors: v, MoldloftBody: mb, MoldloftExamples: me}, nil
		},
	},
}

// ToolTypes lists the tool types that have a profile.
func ToolTypes() []string {
	var out []string
	for _, p := range profiles {
		out = append(out, p.ToolType)
	}
	return out
}

// Select returns the one profile whose rule matches the record. No match, or
// more than one, pauses the item: Waybill never picks for itself.
func Select(rec *record.Record) (*Profile, *fault.Error) {
	var found *Profile
	for i := range profiles {
		if profiles[i].Matches(rec) {
			if found != nil {
				return nil, fault.Unfamiliar("tool_type", "more than one spec-profile matches this item")
			}
			found = &profiles[i]
		}
	}
	if found == nil {
		return nil, fault.Unfamiliar("tool_type", "no spec-profile covers this tool type")
	}
	return found, nil
}
