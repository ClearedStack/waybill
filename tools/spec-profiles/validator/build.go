package validator

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

const (
	maxTables   = 20
	maxCodes    = 500
	maxColumns  = 20
	maxExamples = 50
	maxInputLen = 8192
)

type tool struct {
	Delimiter      string        `json:"delimiter"`
	HasHeader      bool          `json:"has_header"`
	Tables         []LookupTable `json:"tables,omitempty"`
	Columns        []Column      `json:"columns"`
	WorkedExamples []example     `json:"worked_examples"`
}

// LookupTable is an enumerated code table referenced by in_table columns.
type LookupTable struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Citation string   `json:"citation,omitempty"`
	Codes    []string `json:"codes"`
}

// Column is one delimited column rule in a validator tool.
type Column struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Rule     string `json:"rule"`
	Citation string `json:"citation"`
	MinLen   int    `json:"min_len,omitempty"`
	MaxLen   int    `json:"max_len,omitempty"`
	Table    string `json:"table,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
}

type example struct {
	ID        string   `json:"id,omitempty"`
	Label     string   `json:"label,omitempty"`
	Authority string   `json:"authority"`
	Input     string   `json:"input"`
	Expected  Expected `json:"expected"`
}

// Expected is the verified outcome of one validator worked example.
type Expected struct {
	Valid      bool `json:"valid"`
	RowCount   int  `json:"row_count"`
	ErrorCount int  `json:"error_count"`
}

// Schema is the "schema" part of a validator build specification.
type Schema struct {
	ToolType  string        `json:"tool_type"`
	Title     string        `json:"title"`
	Delimiter string        `json:"delimiter"`
	HasHeader bool          `json:"has_header"`
	Tables    []LookupTable `json:"tables,omitempty"`
	Columns   []Column      `json:"columns"`
}

// Vector is one public worked example for a validator tool.
type Vector struct {
	Authority string   `json:"authority"`
	Input     string   `json:"input"`
	Expected  Expected `json:"expected"`
}

// MoldloftBody is the pattern-specific body for Moldloft's validator template.
type MoldloftBody struct {
	Delimiter string        `json:"delimiter"`
	HasHeader bool          `json:"has_header"`
	Tables    []LookupTable `json:"tables,omitempty"`
	Columns   []Column      `json:"columns"`
}

// MoldloftExample is one worked example in Moldloft's validator format.
type MoldloftExample struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Input  string   `json:"input"`
	Expect Expected `json:"expect"`
}

func separatorFor(delim string) (string, bool) {
	switch delim {
	case "comma":
		return ",", true
	case "pipe":
		return "|", true
	case "colon":
		return ":", true
	case "tab":
		return "\t", true
	default:
		return "", false
	}
}

// Build turns a validator record into its build-specification schema and vectors.
func Build(rec *record.Record) (*Schema, []Vector, *fault.Error) {
	schema, vectors, _, _, err := BuildFull(rec)
	return schema, vectors, err
}

// BuildFull turns a validator record into both Waybill's schema/vectors and
// Moldloft's validator body/worked_examples.
func BuildFull(rec *record.Record) (*Schema, []Vector, *MoldloftBody, []MoldloftExample, *fault.Error) {
	var t tool
	if err := strictjson.Decode(rec.Tool, &t); err != nil {
		return nil, nil, nil, nil, fault.Broken("tool", "the tool description is not valid JSON of the expected shape")
	}
	if !record.NameRE.MatchString(t.Delimiter) {
		return nil, nil, nil, nil, fault.Broken("tool.delimiter", "the delimiter is missing or malformed")
	}
	sep, ok := separatorFor(t.Delimiter)
	if !ok {
		return nil, nil, nil, nil, fault.Unfamiliar("tool.delimiter", "the validator profile does not cover this delimiter")
	}

	authorities := map[string]bool{}
	for _, a := range rec.Authority {
		authorities[a.ID] = true
	}

	if len(t.Tables) > maxTables {
		return nil, nil, nil, nil, fault.Broken("tool.tables", "too many lookup tables")
	}
	tableSets := map[string]map[string]bool{}
	var cleanTables []LookupTable
	for i, tb := range t.Tables {
		at := "tool.tables[" + strconv.Itoa(i) + "]"
		if !record.NameRE.MatchString(tb.ID) || tableSets[tb.ID] != nil {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the table id is missing, malformed or repeated")
		}
		if !record.TextRunes(tb.Label, 120) {
			return nil, nil, nil, nil, fault.Broken(at+".label", "the table label is missing or malformed")
		}
		if tb.Citation != "" && !authorities[tb.Citation] {
			return nil, nil, nil, nil, fault.Broken(at+".citation", "the table citation does not name one of the record's authorities")
		}
		if len(tb.Codes) == 0 || len(tb.Codes) > maxCodes {
			return nil, nil, nil, nil, fault.Broken(at+".codes", "the number of table codes is out of range")
		}
		seenCode := map[string]bool{}
		for j, code := range tb.Codes {
			if !record.TextRunes(code, 64) || seenCode[code] {
				return nil, nil, nil, nil, fault.Broken(at+".codes["+strconv.Itoa(j)+"]", "the table code is missing, malformed or repeated")
			}
			seenCode[code] = true
		}
		tableSets[tb.ID] = seenCode
		cleanTables = append(cleanTables, LookupTable{
			ID:       tb.ID,
			Label:    tb.Label,
			Citation: tb.Citation,
			Codes:    append([]string(nil), tb.Codes...),
		})
	}

	if len(t.Columns) == 0 || len(t.Columns) > maxColumns {
		return nil, nil, nil, nil, fault.Broken("tool.columns", "the number of columns is out of range")
	}
	seenCols := map[string]bool{}
	var cleanCols []Column
	for i, col := range t.Columns {
		at := "tool.columns[" + strconv.Itoa(i) + "]"
		if !record.NameRE.MatchString(col.ID) || seenCols[col.ID] {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the column id is missing, malformed or repeated")
		}
		if !record.TextRunes(col.Label, 120) {
			return nil, nil, nil, nil, fault.Broken(at+".label", "the column label is missing or malformed")
		}
		if !record.NameRE.MatchString(col.Rule) {
			return nil, nil, nil, nil, fault.Broken(at+".rule", "the column rule is missing or malformed")
		}
		if !authorities[col.Citation] {
			return nil, nil, nil, nil, fault.Broken(at+".citation", "the column citation does not name one of the record's authorities")
		}
		if col.MinLen < 0 || col.MinLen > 200 {
			return nil, nil, nil, nil, fault.Broken(at+".min_len", "min_len is out of range")
		}
		if col.MaxLen < 0 || col.MaxLen > 200 {
			return nil, nil, nil, nil, fault.Broken(at+".max_len", "max_len is out of range")
		}
		if col.Prefix != "" && !record.TextRunes(col.Prefix, 16) {
			return nil, nil, nil, nil, fault.Broken(at+".prefix", "the column prefix is malformed")
		}

		maxLen := col.MaxLen
		switch col.Rule {
		case "text", "digits", "upper_code":
			if maxLen == 0 {
				maxLen = 64
			}
			if col.MinLen > maxLen {
				return nil, nil, nil, nil, fault.Broken(at+".min_len", "min_len is greater than max_len")
			}
			if col.Table != "" {
				return nil, nil, nil, nil, fault.Broken(at+".table", "table is only allowed when rule is in_table")
			}
		case "in_table":
			if col.Table == "" || tableSets[col.Table] == nil {
				return nil, nil, nil, nil, fault.Broken(at+".table", "table does not name one of the validator tables")
			}
		default:
			return nil, nil, nil, nil, fault.Unfamiliar(at+".rule", "the validator profile does not cover this column rule")
		}
		seenCols[col.ID] = true
		cleanCols = append(cleanCols, Column{
			ID:       col.ID,
			Label:    col.Label,
			Required: col.Required,
			Rule:     col.Rule,
			Citation: col.Citation,
			MinLen:   col.MinLen,
			MaxLen:   maxLen,
			Table:    col.Table,
			Prefix:   col.Prefix,
		})
	}

	if len(t.WorkedExamples) == 0 || len(t.WorkedExamples) > maxExamples {
		return nil, nil, nil, nil, fault.Broken("tool.worked_examples", "the number of worked examples is out of range")
	}

	var vectors []Vector
	var mExamples []MoldloftExample
	seenExIDs := map[string]bool{}
	for i, ex := range t.WorkedExamples {
		at := "tool.worked_examples[" + strconv.Itoa(i) + "]"
		if !authorities[ex.Authority] {
			return nil, nil, nil, nil, fault.Broken(at+".authority", "the worked example does not name one of the record's authorities")
		}
		exID := ex.ID
		if exID == "" {
			exID = fmt.Sprintf("example_%d", i+1)
		} else if !record.NameRE.MatchString(exID) {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the worked example id is malformed")
		}
		if seenExIDs[exID] {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the worked example id is repeated")
		}
		seenExIDs[exID] = true

		exLabel := ex.Label
		if exLabel == "" {
			exLabel = fmt.Sprintf("Worked example %d (%s)", i+1, ex.Authority)
		} else if !record.TextRunes(exLabel, 160) {
			return nil, nil, nil, nil, fault.Broken(at+".label", "the worked example label is malformed")
		}

		if !validMultilineInput(ex.Input) {
			return nil, nil, nil, nil, fault.Broken(at+".input", "the worked example input is empty, oversized or contains disallowed characters")
		}
		if ex.Expected.RowCount < 0 || ex.Expected.RowCount > 500 || ex.Expected.ErrorCount < 0 || ex.Expected.ErrorCount > 1000 {
			return nil, nil, nil, nil, fault.Broken(at+".expected", "the expected counts are out of range")
		}

		got := evaluateValidator(sep, t.HasHeader, cleanCols, tableSets, ex.Input)
		if got != ex.Expected {
			return nil, nil, nil, nil, fault.Broken(at+".expected", "the validator evaluation does not match the expected outcome")
		}

		vectors = append(vectors, Vector{
			Authority: ex.Authority,
			Input:     ex.Input,
			Expected:  got,
		})
		mExamples = append(mExamples, MoldloftExample{
			ID:     exID,
			Label:  exLabel,
			Input:  ex.Input,
			Expect: got,
		})
	}

	schema := &Schema{
		ToolType:  ToolType,
		Title:     rec.Title,
		Delimiter: t.Delimiter,
		HasHeader: t.HasHeader,
		Tables:    cleanTables,
		Columns:   cleanCols,
	}
	mBody := &MoldloftBody{
		Delimiter: t.Delimiter,
		HasHeader: t.HasHeader,
		Tables:    cleanTables,
		Columns:   cleanCols,
	}
	return schema, vectors, mBody, mExamples, nil
}

func validMultilineInput(s string) bool {
	if s == "" || len(s) > maxInputLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cs) ||
			r == '<' || r == '>' || r == unicode.ReplacementChar {
			return false
		}
	}
	lower := strings.ToLower(s)
	return !strings.Contains(lower, "://") && !strings.Contains(lower, "www.")
}

func evaluateValidator(sep string, hasHeader bool, cols []Column, tableSets map[string]map[string]bool, input string) Expected {
	rawLines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	var nonEmpty []string
	for _, line := range rawLines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		nonEmpty = append(nonEmpty, trimmed)
	}
	if len(nonEmpty) == 0 {
		return Expected{Valid: false, RowCount: 0, ErrorCount: 1}
	}

	errCount := 0
	startIdx := 0
	if hasHeader {
		hdr := strings.Split(nonEmpty[0], sep)
		startIdx = 1
		if len(hdr) != len(cols) {
			errCount++
		} else {
			for i, col := range cols {
				if strings.TrimSpace(hdr[i]) != col.ID {
					errCount++
				}
			}
		}
		if len(nonEmpty) == 1 {
			errCount++
			return Expected{Valid: false, RowCount: 0, ErrorCount: errCount}
		}
	}

	rowCount := 0
	for _, row := range nonEmpty[startIdx:] {
		rowCount++
		cells := strings.Split(row, sep)
		if len(cells) != len(cols) {
			errCount++
			continue
		}
		for i, col := range cols {
			val := strings.TrimSpace(cells[i])
			if val == "" {
				if col.Required {
					errCount++
				}
				continue
			}
			if !checkCell(col, val, tableSets) {
				errCount++
			}
		}
	}
	return Expected{
		Valid:      errCount == 0,
		RowCount:   rowCount,
		ErrorCount: errCount,
	}
}

func checkCell(col Column, val string, tableSets map[string]map[string]bool) bool {
	n := utf8.RuneCountInString(val)
	switch col.Rule {
	case "text":
		return n >= col.MinLen && n <= col.MaxLen
	case "digits":
		if n < col.MinLen || n > col.MaxLen {
			return false
		}
		for i := 0; i < len(val); i++ {
			if val[i] < '0' || val[i] > '9' {
				return false
			}
		}
		return true
	case "upper_code":
		if n < col.MinLen || n > col.MaxLen {
			return false
		}
		if col.Prefix != "" && !strings.HasPrefix(val, col.Prefix) {
			return false
		}
		for i := 0; i < len(val); i++ {
			c := val[i]
			if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
		return true
	case "in_table":
		return tableSets[col.Table][val]
	default:
		return false
	}
}
