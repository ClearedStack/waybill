// Package checklist is the spec-profile for compliance checklists whose items
// are answered yes, no, or (when allowed) not applicable.
package checklist

import "github.com/ClearedStack/waybill/internal/record"

// ToolType is the record.ToolType this profile handles.
const ToolType = "checklist"

// Matches is the profile's matching rule.
func Matches(rec *record.Record) bool {
	return rec.ToolType == ToolType
}
