package checklist

import (
	"fmt"
	"strconv"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

const (
	kindYesNo   = "yes_no"
	maxItems    = 60
	maxExamples = 50
)

type tool struct {
	Items          []toolItem `json:"items"`
	WorkedExamples []example  `json:"worked_examples"`
}

type toolItem struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind,omitempty"`
	Required  bool   `json:"required"`
	NAAllowed bool   `json:"na_allowed,omitempty"`
	Citation  string `json:"citation"`
	Help      string `json:"help,omitempty"`
}

type example struct {
	ID        string            `json:"id,omitempty"`
	Label     string            `json:"label,omitempty"`
	Authority string            `json:"authority"`
	Answers   map[string]string `json:"answers"`
	Expected  Expected          `json:"expected"`
}

// Expected is the verified outcome of one checklist worked example.
type Expected struct {
	Status        string `json:"status"`
	UnmetRequired int    `json:"unmet_required"`
	YesCount      int    `json:"yes_count"`
	NoCount       int    `json:"no_count"`
	NACount       int    `json:"na_count"`
}

// Item is one compliance question in a checklist build specification.
type Item struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Required  bool   `json:"required"`
	NAAllowed bool   `json:"na_allowed,omitempty"`
	Citation  string `json:"citation"`
	Help      string `json:"help,omitempty"`
}

// Schema is the "schema" part of a checklist build specification.
type Schema struct {
	ToolType string `json:"tool_type"`
	Title    string `json:"title"`
	Items    []Item `json:"items"`
}

// Vector is one public worked example for a checklist tool.
type Vector struct {
	Authority string            `json:"authority"`
	Answers   map[string]string `json:"inputs"`
	Expected  Expected          `json:"expected"`
}

// MoldloftBody is the pattern-specific body for Moldloft's checklist template.
type MoldloftBody struct {
	Items []Item `json:"items"`
}

// MoldloftExample is one worked example in Moldloft's checklist format.
type MoldloftExample struct {
	ID      string            `json:"id"`
	Label   string            `json:"label"`
	Answers map[string]string `json:"answers"`
	Expect  Expected          `json:"expect"`
}

// Build turns a checklist record into its build-specification schema and vectors.
func Build(rec *record.Record) (*Schema, []Vector, *fault.Error) {
	schema, vectors, _, _, err := BuildFull(rec)
	return schema, vectors, err
}

// BuildFull turns a checklist record into both Waybill's schema/vectors and
// Moldloft's checklist body/worked_examples. Every worked example is evaluated
// deterministically and must agree with its stated outcome.
func BuildFull(rec *record.Record) (*Schema, []Vector, *MoldloftBody, []MoldloftExample, *fault.Error) {
	var t tool
	if err := strictjson.Decode(rec.Tool, &t); err != nil {
		return nil, nil, nil, nil, fault.Broken("tool", "the tool description is not valid JSON of the expected shape")
	}
	if len(t.Items) == 0 || len(t.Items) > maxItems {
		return nil, nil, nil, nil, fault.Broken("tool.items", "the number of checklist items is out of range")
	}

	authorities := map[string]bool{}
	for _, a := range rec.Authority {
		authorities[a.ID] = true
	}

	schema := &Schema{ToolType: ToolType, Title: rec.Title}
	seenIDs := map[string]bool{}
	for i, it := range t.Items {
		at := "tool.items[" + strconv.Itoa(i) + "]"
		if !record.NameRE.MatchString(it.ID) || seenIDs[it.ID] {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the item id is missing, malformed or repeated")
		}
		if !record.TextRunes(it.Label, 240) {
			return nil, nil, nil, nil, fault.Broken(at+".label", "the item label is missing or malformed")
		}
		kind := it.Kind
		if kind == "" {
			kind = kindYesNo
		} else if !record.NameRE.MatchString(kind) {
			return nil, nil, nil, nil, fault.Broken(at+".kind", "the item kind is malformed")
		}
		if kind != kindYesNo {
			return nil, nil, nil, nil, fault.Unfamiliar(at+".kind", "the checklist profile does not cover this item kind")
		}
		if !authorities[it.Citation] {
			return nil, nil, nil, nil, fault.Broken(at+".citation", "the item citation does not name one of the record's authorities")
		}
		if it.Help != "" && !record.TextRunes(it.Help, 300) {
			return nil, nil, nil, nil, fault.Broken(at+".help", "the item help text is malformed")
		}
		seenIDs[it.ID] = true
		schema.Items = append(schema.Items, Item{
			ID:        it.ID,
			Label:     it.Label,
			Kind:      kind,
			Required:  it.Required,
			NAAllowed: it.NAAllowed,
			Citation:  it.Citation,
			Help:      it.Help,
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

		if len(ex.Answers) != len(schema.Items) {
			return nil, nil, nil, nil, fault.Broken(at+".answers", "the worked example does not answer every declared item")
		}
		cleanAnswers := make(map[string]string, len(schema.Items))
		var got Expected
		for _, it := range schema.Items {
			ans, ok := ex.Answers[it.ID]
			if !ok {
				return nil, nil, nil, nil, fault.Broken(at+".answers", "the worked example is missing an answer for "+it.ID)
			}
			switch ans {
			case "yes":
				got.YesCount++
			case "no":
				got.NoCount++
				if it.Required {
					got.UnmetRequired++
				}
			case "na":
				if !it.NAAllowed {
					return nil, nil, nil, nil, fault.Broken(at+".answers."+it.ID, "na is not allowed for this checklist item")
				}
				got.NACount++
			default:
				return nil, nil, nil, nil, fault.Broken(at+".answers."+it.ID, "the answer must be yes, no or na")
			}
			cleanAnswers[it.ID] = ans
		}
		if got.UnmetRequired == 0 {
			got.Status = "complete"
		} else {
			got.Status = "incomplete"
		}

		if ex.Expected.Status != "complete" && ex.Expected.Status != "incomplete" {
			return nil, nil, nil, nil, fault.Broken(at+".expected.status", "the expected status must be complete or incomplete")
		}
		if got != ex.Expected {
			return nil, nil, nil, nil, fault.Broken(at+".expected", "the checklist evaluation does not match the expected outcome")
		}

		vectors = append(vectors, Vector{
			Authority: ex.Authority,
			Answers:   cleanAnswers,
			Expected:  got,
		})
		mExamples = append(mExamples, MoldloftExample{
			ID:      exID,
			Label:   exLabel,
			Answers: cleanAnswers,
			Expect:  got,
		})
	}

	mBody := &MoldloftBody{Items: schema.Items}
	return schema, vectors, mBody, mExamples, nil
}
