// Package rulecore compiles bounded rule graphs and executes explicit inputs.
package rulecore

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

	"competition2026/product/platform/pkg/model"
	"competition2026/product/platform/pkg/precise"
)

func CompileExpression(code string) (*model.ExpressionTree, error) {
	if len(code) > 4096 {
		return nil, errors.New("expression exceeds 4096 bytes")
	}
	expressionParses.Add(1)
	tree, err := parser.ParseExpr(code)
	if err != nil {
		return nil, err
	}
	budget := 256
	var compile func(ast.Expr, int) (*model.ExpressionTree, error)
	compile = func(expr ast.Expr, depth int) (*model.ExpressionTree, error) {
		budget--
		if budget < 0 || depth > 32 {
			return nil, errors.New("expression operation budget exceeded")
		}
		out := &model.ExpressionTree{}
		children := []ast.Expr{}
		switch n := expr.(type) {
		case *ast.ParenExpr:
			return compile(n.X, depth+1)
		case *ast.BasicLit:
			out.Op = "literal"
			if n.Kind == token.STRING {
				out.Value, err = strconv.Unquote(n.Value)
				if err != nil {
					return nil, err
				}
			} else if _, ok := precise.Number(json.Number(n.Value)); ok {
				out.Value = json.Number(n.Value)
			} else {
				return nil, errors.New("invalid numeric literal")
			}
		case *ast.Ident:
			out.Op, out.Name = "variable", n.Name
			if n.Name == "true" || n.Name == "false" || n.Name == "null" {
				out.Op, out.Name = "literal", ""
				if n.Name != "null" {
					out.Value = n.Name == "true"
				}
			}
		case *ast.UnaryExpr:
			ops := map[token.Token]string{token.NOT: "not", token.SUB: "negate", token.ADD: "positive"}
			out.Op = ops[n.Op]
			if out.Op == "" {
				return nil, errors.New("unsupported unary operator")
			}
			children = []ast.Expr{n.X}
		case *ast.BinaryExpr:
			if !contains([]string{"+", "-", "*", "/", "==", "!=", ">", ">=", "<", "<=", "&&", "||"}, n.Op.String()) {
				return nil, errors.New("unsupported binary operator")
			}
			out.Op, children = n.Op.String(), []ast.Expr{n.X, n.Y}
		case *ast.CallExpr:
			fn, ok := n.Fun.(*ast.Ident)
			if !ok || n.Ellipsis.IsValid() {
				return nil, errors.New("function must be a named pure function")
			}
			if !contains([]string{"min", "max", "abs", "round", "choose"}, fn.Name) {
				return nil, fmt.Errorf("function %s is not allowed", fn.Name)
			}
			if len(n.Args) < 1 || len(n.Args) > 16 || (fn.Name == "choose" && len(n.Args) != 3) || ((fn.Name == "abs" || fn.Name == "round") && len(n.Args) != 1) {
				return nil, fmt.Errorf("invalid argument count for %s", fn.Name)
			}
			out.Op, out.Name, children = "call", fn.Name, n.Args
		default:
			return nil, fmt.Errorf("expression syntax %T is not allowed", expr)
		}
		for _, child := range children {
			instruction, err := compile(child, depth+1)
			if err != nil {
				return nil, err
			}
			out.Args = append(out.Args, instruction)
		}
		return out, nil
	}
	return compile(tree, 0)
}

func EvaluateExpression(ctx context.Context, tree *model.ExpressionTree, vars map[string]any) (any, error) {
	budget := 256
	value, err := evaluate(ctx, tree, vars, &budget, 0)
	if number, ok := value.(*big.Rat); ok {
		if number.IsInt() {
			return json.Number(number.Num().String()), err
		}
		decimal, _ := number.Float64()
		if math.IsInf(decimal, 0) || math.IsNaN(decimal) {
			return nil, errors.New("expression result exceeds supported decimal range")
		}
		return decimal, err
	}
	return value, err
}

func numeric(value any) (*big.Rat, error) {
	if number, ok := value.(*big.Rat); ok {
		return new(big.Rat).Set(number), nil
	}
	if number, ok := precise.Number(value); ok {
		return number, nil
	}
	return nil, errors.New("numeric operand required")
}

func Truth(value any) bool { result, ok := value.(bool); return ok && result }

func Compare(a, b any, operator string) (bool, error) {
	if !contains([]string{"==", "!=", ">", ">=", "<", "<="}, operator) {
		return false, errors.New("invalid comparison operator")
	}
	value, err := binary(operator, a, b)
	return Truth(value), err
}

func binary(operator string, a, b any) (any, error) {
	left, le := numeric(a)
	right, re := numeric(b)
	if operator == "==" || operator == "!=" {
		same := false
		if le == nil && re == nil {
			same = left.Cmp(right) == 0
		} else {
			av, ae := json.Marshal(a)
			bv, be := json.Marshal(b)
			if ae != nil || be != nil {
				return nil, errors.Join(ae, be)
			}
			same = string(av) == string(bv)
		}
		if operator == "!=" {
			same = !same
		}
		return same, nil
	}
	if le != nil || re != nil {
		return nil, errors.New("numeric operand required")
	}
	switch operator {
	case "+":
		return left.Add(left, right), nil
	case "-":
		return left.Sub(left, right), nil
	case "*":
		return left.Mul(left, right), nil
	case "/":
		if right.Sign() == 0 {
			return nil, errors.New("division by zero")
		}
		return left.Quo(left, right), nil
	case "<":
		return left.Cmp(right) < 0, nil
	case "<=":
		return left.Cmp(right) <= 0, nil
	case ">":
		return left.Cmp(right) > 0, nil
	case ">=":
		return left.Cmp(right) >= 0, nil
	}
	return nil, errors.New("unsupported binary operator")
}

func evaluate(ctx context.Context, node *model.ExpressionTree, vars map[string]any, budget *int, depth int) (result any, err error) {
	defer func() {
		if number, ok := result.(*big.Rat); ok && (number == nil || number.Num().BitLen() > 4096 || number.Denom().BitLen() > 4096) {
			result, err = nil, errors.New("expression numeric budget exceeded")
		}
	}()
	*budget--
	if *budget < 0 || depth > 32 {
		return nil, errors.New("expression operation budget exceeded")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if node == nil {
		return nil, errors.New("missing expression instruction")
	}
	ev := func(index int) (any, error) {
		if index >= len(node.Args) {
			return nil, errors.New("missing expression operand")
		}
		return evaluate(ctx, node.Args[index], vars, budget, depth+1)
	}
	switch node.Op {
	case "literal":
		return node.Value, nil
	case "variable":
		value, ok := vars[node.Name]
		if !ok {
			return nil, fmt.Errorf("unknown variable %s", node.Name)
		}
		return value, nil
	case "not", "negate", "positive":
		value, err := ev(0)
		if err != nil {
			return nil, err
		}
		if node.Op == "not" {
			if _, ok := value.(bool); !ok {
				return nil, errors.New("boolean operand required")
			}
			return !Truth(value), nil
		}
		number, err := numeric(value)
		if err != nil {
			return nil, err
		}
		if node.Op == "negate" {
			number.Neg(number)
		}
		return number, nil
	case "call":
		if node.Name == "choose" {
			condition, err := ev(0)
			if err != nil {
				return nil, err
			}
			if _, ok := condition.(bool); !ok {
				return nil, errors.New("choose requires a boolean condition")
			}
			if Truth(condition) {
				return ev(1)
			}
			return ev(2)
		}
		values := []*big.Rat{}
		for index := range node.Args {
			value, err := ev(index)
			if err != nil {
				return nil, err
			}
			number, err := numeric(value)
			if err != nil {
				return nil, err
			}
			values = append(values, number)
		}
		if len(values) == 0 {
			return nil, errors.New("function requires arguments")
		}
		value := values[0]
		switch node.Name {
		case "min", "max":
			for _, other := range values[1:] {
				if (node.Name == "min" && other.Cmp(value) < 0) || (node.Name == "max" && other.Cmp(value) > 0) {
					value = other
				}
			}
			return value, nil
		case "abs":
			return value.Abs(value), nil
		case "round":
			quotient, remainder := new(big.Int), new(big.Int)
			quotient.QuoRem(value.Num(), value.Denom(), remainder)
			if new(big.Int).Lsh(new(big.Int).Abs(remainder), 1).Cmp(value.Denom()) >= 0 {
				quotient.Add(quotient, big.NewInt(int64(value.Sign())))
			}
			return new(big.Rat).SetInt(quotient), nil
		}
		return nil, errors.New("unsupported function instruction")
	default:
		left, err := ev(0)
		if err != nil {
			return nil, err
		}
		if node.Op == "&&" || node.Op == "||" {
			if _, ok := left.(bool); !ok {
				return nil, errors.New("boolean operand required")
			}
			if node.Op == "&&" && !Truth(left) || node.Op == "||" && Truth(left) {
				return left, nil
			}
		}
		right, err := ev(1)
		if err != nil {
			return nil, err
		}
		if node.Op == "&&" || node.Op == "||" {
			if _, ok := right.(bool); !ok {
				return nil, errors.New("boolean operand required")
			}
			return right, nil
		}
		return binary(node.Op, left, right)
	}
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
