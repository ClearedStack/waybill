// Package specprofiles is the spec-profiles sub-tool: one profile per tool
// type, each turning an approved record into the schema and vectors of a
// build specification.
package specprofiles

import (
	"github.com/clearedstack/waybill/internal/fault"
	"github.com/clearedstack/waybill/internal/record"
	"github.com/clearedstack/waybill/tools/spec-profiles/calculator"
)

// Profile is one tool-type compiler.
type Profile struct {
	ToolType string
	// Matches is the matching rule.
	Matches func(*record.Record) bool
	// Build is the logic. Schema and vectors must encode to JSON.
	Build func(*record.Record) (schema, vectors any, err *fault.Error)
}

// profiles is every profile in the binary. It must agree with
// tools/registry.yaml.
var profiles = []Profile{
	{
		ToolType: calculator.ToolType,
		Matches:  calculator.Matches,
		Build: func(rec *record.Record) (any, any, *fault.Error) {
			schema, vectors, err := calculator.Build(rec)
			if err != nil {
				return nil, nil, err
			}
			return schema, vectors, nil
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
