// Expressions deliberately expose only arithmetic, comparison and pure functions.
// The AST and operation budgets make evaluation independent of host APIs or loops.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/big"
	"strconv"

	"competition2026/product/platform/internal/store"
)

func Expression(ctx context.Context, code string, vars map[string]any) (any, error) {
	if len(code) > 4096 {
		return nil, errors.New("expression exceeds 4096 bytes")
	}
	tree, e := parser.ParseExpr(code)
	if e != nil {
		return nil, e
	}
	budget := 256
	v, e := evaluate(ctx, tree, vars, &budget, 0)
	if r, ok := v.(*big.Rat); ok {
		if r.IsInt() {
			return json.Number(r.Num().String()), e
		}
		f, _ := r.Float64()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errors.New("expression result exceeds supported decimal range")
		}
		return f, e
	}
	return v, e
}
func numeric(v any) (*big.Rat, error) {
	if r, ok := v.(*big.Rat); ok {
		return new(big.Rat).Set(r), nil
	}
	if r, ok := store.Number(v); ok {
		return r, nil
	}
	return nil, errors.New("numeric operand required")
}
func truth(v any) bool { b, ok := v.(bool); return ok && b }
func evaluate(ctx context.Context, n ast.Expr, vars map[string]any, budget *int, depth int) (result any, err error) {
	defer func() {
		if r, ok := result.(*big.Rat); ok && (r == nil || r.Num().BitLen() > 4096 || r.Denom().BitLen() > 4096) {
			result, err = nil, errors.New("expression numeric budget exceeded")
		}
	}()
	*budget--
	if *budget < 0 || depth > 32 {
		return nil, errors.New("expression operation budget exceeded")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	ev := func(n ast.Expr) (any, error) { return evaluate(ctx, n, vars, budget, depth+1) }
	switch n := n.(type) {
	case *ast.ParenExpr:
		return ev(n.X)
	case *ast.BasicLit:
		if n.Kind == token.STRING {
			return strconv.Unquote(n.Value)
		}
		r, ok := store.Number(json.Number(n.Value))
		if !ok {
			return nil, errors.New("invalid numeric literal")
		}
		return r, nil
	case *ast.Ident:
		switch n.Name {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		v, ok := vars[n.Name]
		if !ok {
			return nil, fmt.Errorf("unknown variable %s", n.Name)
		}
		return v, nil
	case *ast.UnaryExpr:
		v, e := ev(n.X)
		if e != nil {
			return nil, e
		}
		if n.Op == token.NOT {
			if _, ok := v.(bool); !ok {
				return nil, errors.New("boolean operand required")
			}
			return !truth(v), nil
		}
		r, e := numeric(v)
		if e != nil {
			return nil, e
		}
		if n.Op == token.SUB {
			return r.Neg(r), nil
		}
		if n.Op == token.ADD {
			return r, nil
		}
		return nil, errors.New("unsupported unary operator")
	case *ast.BinaryExpr:
		a, e := ev(n.X)
		if e != nil {
			return nil, e
		}
		if n.Op == token.LAND || n.Op == token.LOR {
			if _, ok := a.(bool); !ok {
				return nil, errors.New("boolean operand required")
			}
		}
		if n.Op == token.LAND && !truth(a) {
			return false, nil
		}
		if n.Op == token.LOR && truth(a) {
			return true, nil
		}
		b, e := ev(n.Y)
		if e != nil {
			return nil, e
		}
		if n.Op == token.LAND || n.Op == token.LOR {
			if _, ok := a.(bool); !ok {
				return nil, errors.New("boolean operand required")
			}
			if _, ok := b.(bool); !ok {
				return nil, errors.New("boolean operand required")
			}
			if n.Op == token.LAND {
				return truth(a) && truth(b), nil
			}
			return truth(a) || truth(b), nil
		}
		ar, ae := numeric(a)
		br, be := numeric(b)
		if n.Op == token.EQL || n.Op == token.NEQ {
			same := false
			if ae == nil && be == nil {
				same = ar.Cmp(br) == 0
			} else {
				aa, _ := json.Marshal(a)
				bb, _ := json.Marshal(b)
				same = string(aa) == string(bb)
			}
			if n.Op == token.NEQ {
				same = !same
			}
			return same, nil
		}
		if ae != nil {
			return nil, ae
		}
		if be != nil {
			return nil, be
		}
		switch n.Op {
		case token.ADD:
			return ar.Add(ar, br), nil
		case token.SUB:
			return ar.Sub(ar, br), nil
		case token.MUL:
			return ar.Mul(ar, br), nil
		case token.QUO:
			if br.Sign() == 0 {
				return nil, errors.New("division by zero")
			}
			return ar.Quo(ar, br), nil
		case token.LSS:
			return ar.Cmp(br) < 0, nil
		case token.LEQ:
			return ar.Cmp(br) <= 0, nil
		case token.GTR:
			return ar.Cmp(br) > 0, nil
		case token.GEQ:
			return ar.Cmp(br) >= 0, nil
		}
		return nil, errors.New("unsupported binary operator")
	case *ast.CallExpr:
		fn, ok := n.Fun.(*ast.Ident)
		if !ok {
			return nil, errors.New("function must be a named pure function")
		}
		if len(n.Args) > 16 {
			return nil, errors.New("too many arguments")
		}
		if fn.Name == "choose" {
			if len(n.Args) != 3 {
				return nil, errors.New("choose requires three arguments")
			}
			c, e := ev(n.Args[0])
			if e != nil {
				return nil, e
			}
			if _, ok := c.(bool); !ok {
				return nil, errors.New("choose requires a boolean condition")
			}
			if truth(c) {
				return ev(n.Args[1])
			}
			return ev(n.Args[2])
		}
		values := []*big.Rat{}
		for _, arg := range n.Args {
			v, e := ev(arg)
			if e != nil {
				return nil, e
			}
			r, e := numeric(v)
			if e != nil {
				return nil, e
			}
			values = append(values, r)
		}
		if len(values) == 0 {
			return nil, errors.New("function requires arguments")
		}
		v := values[0]
		switch fn.Name {
		case "min", "max":
			for _, r := range values[1:] {
				if (fn.Name == "min" && r.Cmp(v) < 0) || (fn.Name == "max" && r.Cmp(v) > 0) {
					v = r
				}
			}
			return v, nil
		case "abs":
			if len(values) != 1 {
				return nil, errors.New("abs requires one argument")
			}
			return v.Abs(v), nil
		case "round":
			if len(values) != 1 {
				return nil, errors.New("round requires one argument")
			}
			quotient, remainder := new(big.Int), new(big.Int)
			quotient.QuoRem(v.Num(), v.Denom(), remainder)
			if new(big.Int).Lsh(new(big.Int).Abs(remainder), 1).Cmp(v.Denom()) >= 0 {
				quotient.Add(quotient, big.NewInt(int64(v.Sign())))
			}
			return new(big.Rat).SetInt(quotient), nil
		}
		return nil, fmt.Errorf("function %s is not allowed", fn.Name)
	}
	return nil, fmt.Errorf("expression syntax %T is not allowed", n)
}
