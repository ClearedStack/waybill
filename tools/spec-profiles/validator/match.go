// Package validator is the spec-profile for delimited schedule and line-item
// validators whose columns are checked against deterministic rules and code
// tables.
package validator

import "github.com/ClearedStack/waybill/internal/record"

// ToolType is the record.ToolType this profile handles.
const ToolType = "validator"

// Matches is the profile's matching rule.
func Matches(rec *record.Record) bool {
	return rec.ToolType == ToolType
}
