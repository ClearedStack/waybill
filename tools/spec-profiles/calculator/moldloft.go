package calculator

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
)

const moldloftMaxScale = 9

// MoldloftInput is one calculator input in Moldloft's build-spec-v1 body.
type MoldloftInput struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Unit     string `json:"unit,omitempty"`
	Decimals int    `json:"decimals"`
	Min      string `json:"min"`
	Max      string `json:"max"`
	Default  string `json:"default"`
}

// MoldloftConstant is one constant in Moldloft's build-spec-v1 body.
type MoldloftConstant struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Citation string `json:"citation,omitempty"`
}

// MoldloftStep is one arithmetic step in Moldloft's build-spec-v1 body.
type MoldloftStep struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Op       string   `json:"op"`
	Args     []string `json:"args"`
	Decimals int      `json:"decimals"`
	Unit     string   `json:"unit,omitempty"`
	Citation string   `json:"citation,omitempty"`
}

// MoldloftBody is the pattern-specific body for Moldloft's calculator template.
type MoldloftBody struct {
	Inputs    []MoldloftInput    `json:"inputs"`
	Constants []MoldloftConstant `json:"constants,omitempty"`
	Steps     []MoldloftStep     `json:"steps"`
	Outputs   []string           `json:"outputs"`
}

// MoldloftExample is one worked example in Moldloft's calculator format.
type MoldloftExample struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Inputs map[string]string `json:"inputs"`
	Expect map[string]string `json:"expect"`
}

// lowerToMoldloft translates a validated calculator Schema, parsed formula ASTs,
// and verified Vectors into Moldloft's exact-decimal step representation.
func lowerToMoldloft(rec *record.Record, schema *Schema, formulas map[string]node, vectors []Vector) (*MoldloftBody, []MoldloftExample, *fault.Error) {
	usedNames := map[string]bool{}
	for _, in := range schema.Inputs {
		usedNames[in.Name] = true
	}
	for _, out := range schema.Outputs {
		usedNames[out.Name] = true
	}

	firstCitation := rec.Authority[0].ID

	// Determine decimal scale per input and per output from bounds and examples.
	inputScale := map[string]int{}
	for _, in := range schema.Inputs {
		sc := maxInt(decimalPlaces(string(in.Min)), decimalPlaces(string(in.Max)))
		for _, v := range vectors {
			sc = maxInt(sc, decimalPlaces(string(v.Inputs[in.Name])))
		}
		if sc > moldloftMaxScale {
			sc = moldloftMaxScale
		}
		inputScale[in.Name] = sc
	}

	outputScale := map[string]int{}
	for _, out := range schema.Outputs {
		sc := 0
		for _, v := range vectors {
			sc = maxInt(sc, decimalPlaces(string(v.Expected[out.Name])))
		}
		if sc > moldloftMaxScale {
			sc = moldloftMaxScale
		}
		outputScale[out.Name] = sc
	}

	body := &MoldloftBody{}
	for _, in := range schema.Inputs {
		sc := inputScale[in.Name]
		_, minRat, _ := parseNum(string(in.Min))
		_, maxRat, _ := parseNum(string(in.Max))
		_, defRat, _ := parseNum(string(vectors[0].Inputs[in.Name]))
		body.Inputs = append(body.Inputs, MoldloftInput{
			ID:       in.Name,
			Label:    humanLabel(in.Name),
			Kind:     "number",
			Unit:     in.Unit,
			Decimals: sc,
			Min:      formatFixedRat(minRat, sc),
			Max:      formatFixedRat(maxRat, sc),
			Default:  formatFixedRat(defRat, sc),
		})
	}

	constByVal := map[string]string{}
	nextIndex := 1
	freshID := func(prefix string) string {
		for {
			cand := fmt.Sprintf("%s_%d", prefix, nextIndex)
			nextIndex++
			if !usedNames[cand] {
				usedNames[cand] = true
				return cand
			}
		}
	}
	ensureConst := func(val string) string {
		if id, ok := constByVal[val]; ok {
			return id
		}
		id := freshID("const")
		constByVal[val] = id
		body.Constants = append(body.Constants, MoldloftConstant{
			ID:       id,
			Label:    "Constant " + val,
			Value:    val,
			Citation: firstCitation,
		})
		return id
	}

	var lowerExpr func(n node, targetID, targetLabel, targetUnit string, targetDecimals int, isTop bool) string
	lowerExpr = func(n node, targetID, targetLabel, targetUnit string, targetDecimals int, isTop bool) string {
		stepMeta := func(op string, args []string) string {
			id := targetID
			label := targetLabel
			unit := targetUnit
			dec := targetDecimals
			cit := firstCitation
			if !isTop {
				id = freshID("step")
				label = "Intermediate step " + id
				unit = ""
				dec = moldloftMaxScale
				cit = ""
			}
			body.Steps = append(body.Steps, MoldloftStep{
				ID:       id,
				Label:    label,
				Op:       op,
				Args:     args,
				Decimals: dec,
				Unit:     unit,
				Citation: cit,
			})
			return id
		}

		switch n := n.(type) {
		case numNode:
			cid := ensureConst(n.text)
			if !isTop {
				return cid
			}
			zeroID := ensureConst("0")
			return stepMeta("add", []string{cid, zeroID})
		case refNode:
			if !isTop {
				return n.name
			}
			zeroID := ensureConst("0")
			return stepMeta("add", []string{n.name, zeroID})
		case negNode:
			zeroID := ensureConst("0")
			arg := lowerExpr(n.x, "", "", "", moldloftMaxScale, false)
			return stepMeta("sub", []string{zeroID, arg})
		case binNode:
			l := lowerExpr(n.l, "", "", "", moldloftMaxScale, false)
			r := lowerExpr(n.r, "", "", "", moldloftMaxScale, false)
			var op string
			switch n.op {
			case '+':
				op = "add"
			case '-':
				op = "sub"
			case '*':
				op = "mul"
			case '/':
				op = "div"
			}
			return stepMeta(op, []string{l, r})
		case callNode:
			args := make([]string, len(n.args))
			for i, a := range n.args {
				args[i] = lowerExpr(a, "", "", "", moldloftMaxScale, false)
			}
			return stepMeta(n.fn, args)
		}
		return ""
	}

	for _, out := range schema.Outputs {
		sc := outputScale[out.Name]
		lowerExpr(formulas[out.Name], out.Name, humanLabel(out.Name), out.Unit, sc, true)
		body.Outputs = append(body.Outputs, out.Name)
	}

	if len(body.Steps) > 80 || len(body.Constants) > 40 || len(body.Outputs) > 10 {
		return nil, nil, fault.Broken("tool.outputs", "the lowered calculator exceeds step or constant bounds")
	}

	var examples []MoldloftExample
	for i, v := range vectors {
		inMap := make(map[string]string, len(v.Inputs))
		var keys []string
		for k := range v.Inputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, r, _ := parseNum(string(v.Inputs[k]))
			inMap[k] = formatFixedRat(r, inputScale[k])
		}
		expMap := make(map[string]string, len(v.Expected))
		for _, out := range schema.Outputs {
			_, r, _ := parseNum(string(v.Expected[out.Name]))
			expMap[out.Name] = formatFixedRat(r, outputScale[out.Name])
		}
		// Verify step-by-step rounded evaluation matches expMap so Moldloft's
		// own rounded evaluator agrees on every worked example.
		stepVals, ok := evalMoldloftSteps(body, inMap)
		if !ok {
			return nil, nil, fault.Broken("tool.worked_examples["+strconv.Itoa(i)+"]", "stepwise evaluation failed")
		}
		for _, out := range schema.Outputs {
			if stepVals[out.Name] != expMap[out.Name] {
				return nil, nil, fault.Broken("tool.worked_examples["+strconv.Itoa(i)+"].expected", "rounded step evaluation disagrees with expected value")
			}
		}
		examples = append(examples, MoldloftExample{
			ID:     fmt.Sprintf("example_%d", i+1),
			Label:  fmt.Sprintf("Worked example %d (%s)", i+1, v.Authority),
			Inputs: inMap,
			Expect: expMap,
		})
	}
	return body, examples, nil
}

func evalMoldloftSteps(body *MoldloftBody, inputs map[string]string) (map[string]string, bool) {
	vals := map[string]*big.Rat{}
	for k, s := range inputs {
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil, false
		}
		vals[k] = r
	}
	for _, c := range body.Constants {
		r, ok := new(big.Rat).SetString(c.Value)
		if !ok {
			return nil, false
		}
		vals[c.ID] = r
	}
	formatted := map[string]string{}
	for _, st := range body.Steps {
		args := make([]*big.Rat, len(st.Args))
		for i, name := range st.Args {
			v, ok := vals[name]
			if !ok {
				return nil, false
			}
			args[i] = v
		}
		var res *big.Rat
		switch st.Op {
		case "add":
			res = new(big.Rat).Set(args[0])
			for _, a := range args[1:] {
				res.Add(res, a)
			}
		case "sub":
			res = new(big.Rat).Sub(args[0], args[1])
		case "mul":
			res = new(big.Rat).Set(args[0])
			for _, a := range args[1:] {
				res.Mul(res, a)
			}
		case "div":
			if args[1].Sign() == 0 {
				return nil, false
			}
			res = new(big.Rat).Quo(args[0], args[1])
		case "min":
			res = new(big.Rat).Set(args[0])
			for _, a := range args[1:] {
				if a.Cmp(res) < 0 {
					res = new(big.Rat).Set(a)
				}
			}
		case "max":
			res = new(big.Rat).Set(args[0])
			for _, a := range args[1:] {
				if a.Cmp(res) > 0 {
					res = new(big.Rat).Set(a)
				}
			}
		default:
			return nil, false
		}
		roundedStr := formatFixedRat(res, st.Decimals)
		roundedRat, ok := new(big.Rat).SetString(roundedStr)
		if !ok {
			return nil, false
		}
		vals[st.ID] = roundedRat
		formatted[st.ID] = roundedStr
	}
	return formatted, true
}

// formatFixedRat rounds r half-away-from-zero to scale decimal places and
// formats it as a plain decimal string.
func formatFixedRat(r *big.Rat, scale int) string {
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)

	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaledNum := new(big.Int).Mul(abs.Num(), pow)
	den := abs.Denom()

	quo, rem := new(big.Int).QuoRem(scaledNum, den, new(big.Int))
	twiceRem := new(big.Int).Mul(rem, big.NewInt(2))
	if twiceRem.Cmp(den) >= 0 {
		quo.Add(quo, big.NewInt(1))
	}
	if quo.Sign() == 0 {
		neg = false
	}

	digits := quo.String()
	var out string
	if scale == 0 {
		out = digits
	} else {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		split := len(digits) - scale
		out = digits[:split] + "." + digits[split:]
	}
	if neg {
		return "-" + out
	}
	return out
}

func decimalPlaces(s string) int {
	_, frac, ok := strings.Cut(s, ".")
	if !ok {
		return 0
	}
	frac = strings.TrimRight(frac, "0")
	return len(frac)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func humanLabel(id string) string {
	parts := strings.Split(id, "_")
	for i, p := range parts {
		if i == 0 && len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
