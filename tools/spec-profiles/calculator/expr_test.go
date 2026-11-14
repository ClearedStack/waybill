package calculator

import (
	"math/big"
	"testing"
)

func TestFormula(t *testing.T) {
	names := map[string]bool{"a": true, "b": true, "c": true}
	env := map[string]*big.Rat{"a": big.NewRat(7, 1), "b": big.NewRat(2, 1), "c": big.NewRat(1, 2)}
	cases := []struct {
		src, canon, value string
	}{
		{"a+b", "a + b", "9"},
		{"a - b - c", "a - b - c", "9/2"},
		{"a-(b-c)", "a - (b - c)", "11/2"},
		{"(a+b)*c", "(a + b) * c", "9/2"},
		{"a+b*c", "a + b * c", "8"},
		{"a/b/b", "a / b / b", "7/4"},
		{"a/(b*b)", "a / (b * b)", "7/4"},
		{"-a+b", "-a + b", "-5"},
		{"-(a+b)", "-(a + b)", "-9"},
		{"a*-b", "a * -b", "-14"},
		{"min(a,b*10.50,  100)", "min(a, b * 10.5, 100)", "7"},
		{"max(min(a,b),c)", "max(min(a, b), c)", "2"},
		{"((a))", "a", "7"},
		{"0.1+0.2", "0.1 + 0.2", "3/10"},
	}
	for _, c := range cases {
		tree, problem := parseFormula(c.src, names)
		if problem != exprOK {
			t.Errorf("%q: problem %d", c.src, problem)
			continue
		}
		if got := format(tree); got != c.canon {
			t.Errorf("%q: canonical form %q, want %q", c.src, got, c.canon)
		}
		again, _ := parseFormula(format(tree), names)
		if again == nil || format(again) != c.canon {
			t.Errorf("%q: canonical form does not parse back to itself", c.src)
		}
		got, ok := eval(tree, env)
		if !ok || got.RatString() != c.value {
			t.Errorf("%q: value %v, want %s", c.src, got, c.value)
		}
	}
}

func TestFormulaProblems(t *testing.T) {
	names := map[string]bool{"a": true}
	cases := []struct {
		src  string
		want exprProblem
	}{
		{"", exprSyntax},
		{"a +", exprSyntax},
		{"a a", exprSyntax},
		{"(a", exprSyntax},
		{"a)", exprSyntax},
		{"a ^ 2", exprSyntax},
		{"A", exprSyntax},
		{"01 + a", exprSyntax},
		{"1e3", exprSyntax},
		{"1.2.3", exprSyntax},
		{"min(a)", exprSyntax},
		{"min(a,)", exprSyntax},
		{"b + 1", exprUnknownName},
		{"round(a, 2)", exprUnknownFunc},
		{"sqrt(a)", exprUnknownFunc},
	}
	for _, c := range cases {
		if _, got := parseFormula(c.src, names); got != c.want {
			t.Errorf("%q: problem %d, want %d", c.src, got, c.want)
		}
	}
	tree, _ := parseFormula("1 / (a - a)", names)
	if _, ok := eval(tree, map[string]*big.Rat{"a": big.NewRat(1, 1)}); ok {
		t.Error("division by zero was not reported")
	}
}

func TestParseNum(t *testing.T) {
	good := map[string]string{"0": "0", "-0": "0", "1.50": "1.5", "2.000": "2", "-12.34": "-12.34", "0.5": "0.5"}
	for in, want := range good {
		if got, _, ok := parseNum(in); !ok || got != want {
			t.Errorf("parseNum(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "01", "1.", ".5", "1e3", "+1", "1,5", "NaN", "1234567890123456", "0.1234567890"} {
		if _, _, ok := parseNum(in); ok {
			t.Errorf("parseNum(%q) was accepted", in)
		}
	}
}
