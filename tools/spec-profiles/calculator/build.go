package calculator

import (
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

const (
	typeNumber  = "number"
	maxInputs   = 32
	maxOutputs  = 16
	maxExamples = 64
)

var (
	typeRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	unitRE = regexp.MustCompile(`^[A-Za-z0-9%/._-]{1,24}$`)
)

// tool is the "tool" part of a calculator record.
type tool struct {
	Inputs         []toolInput  `json:"inputs"`
	Outputs        []toolOutput `json:"outputs"`
	WorkedExamples []example    `json:"worked_examples"`
}

type toolInput struct {
	Name string      `json:"name"`
	Type string      `json:"type"`
	Unit string      `json:"unit"`
	Min  json.Number `json:"min"`
	Max  json.Number `json:"max"`
}

type toolOutput struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Unit    string `json:"unit"`
	Formula string `json:"formula"`
}

type example struct {
	Authority string                 `json:"authority"`
	Inputs    map[string]json.Number `json:"inputs"`
	Expected  map[string]json.Number `json:"expected"`
}

// Schema is the "schema" part of a calculator build specification.
type Schema struct {
	ToolType string   `json:"tool_type"`
	Title    string   `json:"title"`
	Inputs   []Input  `json:"inputs"`
	Outputs  []Output `json:"outputs"`
}

// Input is one bounded numeric input.
type Input struct {
	Name string      `json:"name"`
	Type string      `json:"type"`
	Unit string      `json:"unit"`
	Min  json.Number `json:"min"`
	Max  json.Number `json:"max"`
}

// Output is one numeric output and the formula that gives it.
type Output struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Unit    string `json:"unit"`
	Formula string `json:"formula"`
}

// Vector is one public worked example: inputs and the outputs they must give.
type Vector struct {
	Authority string                 `json:"authority"`
	Inputs    map[string]json.Number `json:"inputs"`
	Expected  map[string]json.Number `json:"expected"`
}

type bound struct{ min, max *big.Rat }

// Build turns a calculator record into the schema and vectors of its build
// specification. Every worked example is recomputed from the formulas and
// must agree exactly.
func Build(rec *record.Record) (*Schema, []Vector, *fault.Error) {
	var t tool
	if err := strictjson.Decode(rec.Tool, &t); err != nil {
		return nil, nil, fault.Broken("tool", "the tool description is not valid JSON of the expected shape")
	}
	schema := &Schema{ToolType: ToolType, Title: rec.Title}

	if len(t.Inputs) == 0 || len(t.Inputs) > maxInputs {
		return nil, nil, fault.Broken("tool.inputs", "the number of inputs is out of range")
	}
	names := map[string]bool{}
	bounds := map[string]bound{}
	for i, in := range t.Inputs {
		at := "tool.inputs[" + strconv.Itoa(i) + "]"
		if !record.NameRE.MatchString(in.Name) || names[in.Name] || functions[in.Name] {
			return nil, nil, fault.Broken(at+".name", "the input name is missing, malformed, reserved or repeated")
		}
		if f := checkType(at, in.Type, in.Unit); f != nil {
			return nil, nil, f
		}
		minText, minVal, ok := parseNum(string(in.Min))
		if !ok {
			return nil, nil, fault.Broken(at+".min", "the lower bound is missing or not a plain decimal")
		}
		maxText, maxVal, ok := parseNum(string(in.Max))
		if !ok {
			return nil, nil, fault.Broken(at+".max", "the upper bound is missing or not a plain decimal")
		}
		if minVal.Cmp(maxVal) > 0 {
			return nil, nil, fault.Broken(at, "the lower bound is above the upper bound")
		}
		names[in.Name] = true
		bounds[in.Name] = bound{minVal, maxVal}
		schema.Inputs = append(schema.Inputs, Input{in.Name, in.Type, in.Unit, json.Number(minText), json.Number(maxText)})
	}

	if len(t.Outputs) == 0 || len(t.Outputs) > maxOutputs {
		return nil, nil, fault.Broken("tool.outputs", "the number of outputs is out of range")
	}
	formulas := map[string]node{}
	for i, out := range t.Outputs {
		at := "tool.outputs[" + strconv.Itoa(i) + "]"
		if !record.NameRE.MatchString(out.Name) || names[out.Name] || formulas[out.Name] != nil || functions[out.Name] {
			return nil, nil, fault.Broken(at+".name", "the output name is missing, malformed, reserved or repeated")
		}
		if f := checkType(at, out.Type, out.Unit); f != nil {
			return nil, nil, f
		}
		tree, problem := parseFormula(out.Formula, names)
		switch problem {
		case exprSyntax:
			return nil, nil, fault.Broken(at+".formula", "the formula is missing or malformed")
		case exprUnknownName:
			return nil, nil, fault.Broken(at+".formula", "the formula uses a name that is not a declared input")
		case exprUnknownFunc:
			return nil, nil, fault.Unfamiliar(at+".formula", "the formula uses a function the calculator profile does not cover")
		}
		formulas[out.Name] = tree
		schema.Outputs = append(schema.Outputs, Output{out.Name, out.Type, out.Unit, format(tree)})
	}

	if len(t.WorkedExamples) == 0 || len(t.WorkedExamples) > maxExamples {
		return nil, nil, fault.Broken("tool.worked_examples", "the number of worked examples is out of range")
	}
	authorities := map[string]bool{}
	for _, a := range rec.Authority {
		authorities[a.ID] = true
	}
	var vectors []Vector
	for i, ex := range t.WorkedExamples {
		at := "tool.worked_examples[" + strconv.Itoa(i) + "]"
		if !authorities[ex.Authority] {
			return nil, nil, fault.Broken(at+".authority", "the worked example does not name one of the record's authorities")
		}
		if len(ex.Inputs) != len(schema.Inputs) {
			return nil, nil, fault.Broken(at+".inputs", "the worked example does not give exactly the declared inputs")
		}
		v := Vector{ex.Authority, map[string]json.Number{}, map[string]json.Number{}}
		env := map[string]*big.Rat{}
		for j, in := range schema.Inputs {
			text, val, ok := parseNum(string(ex.Inputs[in.Name]))
			if !ok {
				return nil, nil, fault.Broken(at+".inputs", "the value for input "+strconv.Itoa(j)+" is missing or not a plain decimal")
			}
			if b := bounds[in.Name]; val.Cmp(b.min) < 0 || val.Cmp(b.max) > 0 {
				return nil, nil, fault.Broken(at+".inputs", "the value for input "+strconv.Itoa(j)+" is outside its bounds")
			}
			env[in.Name] = val
			v.Inputs[in.Name] = json.Number(text)
		}
		if len(ex.Expected) != len(schema.Outputs) {
			return nil, nil, fault.Broken(at+".expected", "the worked example does not give exactly the declared outputs")
		}
		for j, out := range schema.Outputs {
			text, want, ok := parseNum(string(ex.Expected[out.Name]))
			if !ok {
				return nil, nil, fault.Broken(at+".expected", "the value for output "+strconv.Itoa(j)+" is missing or not a plain decimal")
			}
			got, ok := eval(formulas[out.Name], env)
			if !ok {
				return nil, nil, fault.Broken(at+".expected", "the formula for output "+strconv.Itoa(j)+" divides by zero for this example")
			}
			if got.Cmp(want) != 0 {
				return nil, nil, fault.Broken(at+".expected", "the formula for output "+strconv.Itoa(j)+" does not give the expected value")
			}
			v.Expected[out.Name] = json.Number(text)
		}
		vectors = append(vectors, v)
	}
	return schema, vectors, nil
}

// BuildFull turns a calculator record into both Waybill's SPEC.md schema and
// vectors and Moldloft's build-spec-v1 calculator body and worked examples.
func BuildFull(rec *record.Record) (*Schema, []Vector, *MoldloftBody, []MoldloftExample, *fault.Error) {
	schema, vectors, err := Build(rec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	names := make(map[string]bool, len(schema.Inputs))
	for _, in := range schema.Inputs {
		names[in.Name] = true
	}
	formulas := make(map[string]node, len(schema.Outputs))
	for _, out := range schema.Outputs {
		tree, _ := parseFormula(out.Formula, names)
		formulas[out.Name] = tree
	}
	mBody, mExamples, err := lowerToMoldloft(rec, schema, formulas, vectors)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return schema, vectors, mBody, mExamples, nil
}

// checkType accepts the one value type this profile covers. A well-formed
// type it does not know is unfamiliar, not broken.
func checkType(at, typ, unit string) *fault.Error {
	if !typeRE.MatchString(typ) {
		return fault.Broken(at+".type", "the type is missing or malformed")
	}
	if typ != typeNumber {
		return fault.Unfamiliar(at+".type", "the calculator profile does not cover this value type")
	}
	if !unitRE.MatchString(unit) {
		return fault.Broken(at+".unit", "the unit is missing or malformed")
	}
	return nil
}
