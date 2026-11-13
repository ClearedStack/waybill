// Package fault holds the three public error codes Waybill may report.
//
// Messages are fixed strings written in this repository. A fault never
// carries a value taken from the input, a file path, a host name or a
// credential: only a code, a fixed message and the position of the field.
package fault

// Code is one of the three public error codes.
type Code string

const (
	// Timeout: something outside took too long.
	Timeout Code = "E_TIMEOUT"
	// Parse: the data was broken.
	Parse Code = "E_PARSE"
	// NeedsSubtool: the data was valid and unfamiliar.
	NeedsSubtool Code = "E_NEEDS_SUBTOOL"
)

// Error is a sanitized failure. Field is a position such as
// "tool.inputs[1].min"; it is built from field names and indexes fixed in the
// code, never from input values.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func (e *Error) Error() string {
	if e.Field == "" {
		return string(e.Code) + ": " + e.Message
	}
	return string(e.Code) + ": " + e.Field + ": " + e.Message
}

// Broken reports E_PARSE.
func Broken(field, message string) *Error {
	return &Error{Code: Parse, Message: message, Field: field}
}

// Unfamiliar reports E_NEEDS_SUBTOOL.
func Unfamiliar(field, message string) *Error {
	return &Error{Code: NeedsSubtool, Message: message, Field: field}
}
