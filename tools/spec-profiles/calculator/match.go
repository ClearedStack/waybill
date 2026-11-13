// Package calculator is the spec-profile for calculators: tools that take
// bounded numbers in and give numbers out by a fixed formula.
//
// It has three parts: the matching rule (this file), the logic (build.go and
// expr.go) and the fixtures (testdata).
package calculator

import "github.com/clearedstack/waybill/internal/record"

// ToolType is the tool type this profile covers.
const ToolType = "calculator"

// Matches is the matching rule: the record asks for a calculator.
func Matches(rec *record.Record) bool {
	return rec.ToolType == ToolType
}
