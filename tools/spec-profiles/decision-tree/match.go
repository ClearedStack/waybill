// Package decisiontree is the spec-profile for acyclic branching determination
// trees whose questions lead deterministically to cited eligibility outcomes.
package decisiontree

import "github.com/ClearedStack/waybill/internal/record"

// ToolType is the record.ToolType this profile handles.
const ToolType = "decision-tree"

// Matches is the profile's matching rule.
func Matches(rec *record.Record) bool {
	return rec.ToolType == ToolType
}
