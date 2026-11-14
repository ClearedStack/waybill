package calculator

import (
	"math/big"
	"regexp"
	"strings"
)

// A formula is arithmetic over the declared inputs:
//
//	expr    = term { ("+" | "-") term }
//	term    = unary { ("*" | "/") unary }
//	unary   = "-" unary | primary
//	primary = number | name | func "(" expr { "," expr } ")" | "(" expr ")"
//
// The only functions are min and max. All arithmetic is exact (math/big.Rat),
// so a result never depends on the machine it is computed on.

const (
	maxFormulaBytes = 512
	maxFormulaDepth = 32
	maxFuncArgs     = 8
)

var functions = map[string]bool{"min": true, "max": true}

type node interface{}

type (
	numNode struct{ text string }
	refNode struct{ name string }
	negNode struct{ x node }
	binNode struct {
		op   byte
		l, r node
	}
	callNode struct {
		fn   string
		args []node
	}
)

type exprProblem int

const (
	exprOK exprProblem = iota
	exprSyntax
	exprUnknownName
	exprUnknownFunc
)

// numRE is a plain decimal: no exponent, no leading zeros, bounded length.
var numRE = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]{1,9})?$`)

// parseNum returns the canonical text of a decimal ("1.50" becomes "1.5",
// "-0" becomes "0") and its exact value.
func parseNum(s string) (string, *big.Rat, bool) {
	if !numRE.MatchString(s) {
		return "", nil, false
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "-0" {
		s = "0"
	}
	r, ok := new(big.Rat).SetString(s)
	return s, r, ok
}

type token struct {
	kind byte // 'n' number, 'i' name, or the operator itself
	text string
}

func lex(src string) ([]token, bool) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ':
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			toks = append(toks, token{'n', src[i:j]})
			i = j
		case c >= 'a' && c <= 'z':
			j := i
			for j < len(src) && (src[j] >= 'a' && src[j] <= 'z' || src[j] >= '0' && src[j] <= '9' || src[j] == '_') {
				j++
			}
			toks = append(toks, token{'i', src[i:j]})
			i = j
		case strings.IndexByte("+-*/(),", c) >= 0:
			toks = append(toks, token{c, string(c)})
			i++
		default:
			return nil, false
		}
	}
	return toks, true
}

type parser struct {
	toks    []token
	pos     int
	names   map[string]bool
	problem exprProblem
}

func (p *parser) fail(why exprProblem) node {
	if p.problem == exprOK {
		p.problem = why
	}
	return nil
}

func (p *parser) peek() byte {
	if p.pos < len(p.toks) {
		return p.toks[p.pos].kind
	}
	return 0
}

// parseFormula parses src. Names must be declared inputs.
func parseFormula(src string, names map[string]bool) (node, exprProblem) {
	if src == "" || len(src) > maxFormulaBytes {
		return nil, exprSyntax
	}
	toks, ok := lex(src)
	if !ok {
		return nil, exprSyntax
	}
	p := &parser{toks: toks, names: names}
	n := p.expr(0)
	if p.problem == exprOK && p.pos != len(p.toks) {
		p.fail(exprSyntax)
	}
	if p.problem != exprOK {
		return nil, p.problem
	}
	return n, exprOK
}

func (p *parser) expr(depth int) node {
	if depth > maxFormulaDepth {
		return p.fail(exprSyntax)
	}
	left := p.term(depth)
	for p.problem == exprOK && (p.peek() == '+' || p.peek() == '-') {
		op := p.toks[p.pos].kind
		p.pos++
		left = binNode{op, left, p.term(depth)}
	}
	return left
}

func (p *parser) term(depth int) node {
	left := p.unary(depth)
	for p.problem == exprOK && (p.peek() == '*' || p.peek() == '/') {
		op := p.toks[p.pos].kind
		p.pos++
		left = binNode{op, left, p.unary(depth)}
	}
	return left
}

func (p *parser) unary(depth int) node {
	if depth > maxFormulaDepth {
		return p.fail(exprSyntax)
	}
	if p.peek() == '-' {
		p.pos++
		return negNode{p.unary(depth + 1)}
	}
	return p.primary(depth)
}

func (p *parser) primary(depth int) node {
	switch p.peek() {
	case 'n':
		text, _, ok := parseNum(p.toks[p.pos].text)
		if !ok {
			return p.fail(exprSyntax)
		}
		p.pos++
		return numNode{text}
	case 'i':
		name := p.toks[p.pos].text
		p.pos++
		if p.peek() != '(' {
			if !p.names[name] {
				return p.fail(exprUnknownName)
			}
			return refNode{name}
		}
		p.pos++
		if !functions[name] {
			return p.fail(exprUnknownFunc)
		}
		args := []node{p.expr(depth + 1)}
		for p.problem == exprOK && p.peek() == ',' {
			p.pos++
			args = append(args, p.expr(depth+1))
		}
		if p.peek() != ')' || len(args) < 2 || len(args) > maxFuncArgs {
			return p.fail(exprSyntax)
		}
		p.pos++
		return callNode{name, args}
	case '(':
		p.pos++
		inner := p.expr(depth + 1)
		if p.peek() != ')' {
			return p.fail(exprSyntax)
		}
		p.pos++
		return inner
	}
	return p.fail(exprSyntax)
}

// eval computes n exactly. It reports false on division by zero.
func eval(n node, env map[string]*big.Rat) (*big.Rat, bool) {
	switch n := n.(type) {
	case numNode:
		_, r, ok := parseNum(n.text)
		return r, ok
	case refNode:
		r, ok := env[n.name]
		return r, ok
	case negNode:
		x, ok := eval(n.x, env)
		if !ok {
			return nil, false
		}
		return new(big.Rat).Neg(x), true
	case binNode:
		l, ok := eval(n.l, env)
		if !ok {
			return nil, false
		}
		r, ok := eval(n.r, env)
		if !ok {
			return nil, false
		}
		out := new(big.Rat)
		switch n.op {
		case '+':
			return out.Add(l, r), true
		case '-':
			return out.Sub(l, r), true
		case '*':
			return out.Mul(l, r), true
		case '/':
			if r.Sign() == 0 {
				return nil, false
			}
			return out.Quo(l, r), true
		}
	case callNode:
		var best *big.Rat
		for _, a := range n.args {
			v, ok := eval(a, env)
			if !ok {
				return nil, false
			}
			if best == nil || (n.fn == "min" && v.Cmp(best) < 0) || (n.fn == "max" && v.Cmp(best) > 0) {
				best = v
			}
		}
		return best, best != nil
	}
	return nil, false
}

func prec(n node) int {
	switch n := n.(type) {
	case binNode:
		if n.op == '+' || n.op == '-' {
			return 1
		}
		return 2
	case negNode:
		return 3
	}
	return 4
}

// format writes n in one canonical spelling: single spaces around binary
// operators and only the parentheses the grouping needs.
func format(n node) string {
	switch n := n.(type) {
	case numNode:
		return n.text
	case refNode:
		return n.name
	case negNode:
		return "-" + wrap(n.x, prec(n.x) < 3)
	case binNode:
		p := prec(n)
		return wrap(n.l, prec(n.l) < p) + " " + string(n.op) + " " + wrap(n.r, prec(n.r) <= p)
	case callNode:
		parts := make([]string, len(n.args))
		for i, a := range n.args {
			parts[i] = format(a)
		}
		return n.fn + "(" + strings.Join(parts, ", ") + ")"
	}
	return ""
}

func wrap(n node, parens bool) string {
	if parens {
		return "(" + format(n) + ")"
	}
	return format(n)
}
