package rulecore

import (
	"encoding/json"
	"errors"
	"fmt"

	"competition2026/product/platform/pkg/model"
)

func expressionType(tree *model.ExpressionTree) (string, error) {
	if tree == nil {
		return "any", errors.New("missing expression instruction")
	}
	if tree.Op == "literal" {
		switch tree.Value.(type) {
		case bool:
			return "boolean", nil
		case string:
			return "string", nil
		case json.Number, int, int64, float64:
			return "number", nil
		default:
			return "any", nil
		}
	}
	if tree.Op == "variable" {
		switch tree.Name {
		case "good", "fresh", "active":
			return "boolean", nil
		case "quality":
			return "string", nil
		case "sum", "count", "min", "max", "avg":
			return "number", nil
		default:
			return "any", nil
		}
	}
	children := []string{}
	for _, child := range tree.Args {
		kind, err := expressionType(child)
		if err != nil {
			return "", err
		}
		children = append(children, kind)
	}
	require := func(index int, kind string) error {
		if index >= len(children) {
			return errors.New("missing expression operand")
		}
		if children[index] != "any" && children[index] != kind {
			return fmt.Errorf("%s operand required for %s", kind, tree.Op)
		}
		return nil
	}
	switch tree.Op {
	case "==", "!=":
		return "boolean", nil
	case "not", "&&", "||":
		for index := range children {
			if err := require(index, "boolean"); err != nil {
				return "", err
			}
		}
		return "boolean", nil
	case "call":
		if tree.Name == "choose" {
			if err := require(0, "boolean"); err != nil {
				return "", err
			}
			if len(children) != 3 {
				return "", errors.New("choose requires three arguments")
			}
			if children[1] == children[2] {
				return children[1], nil
			}
			return "any", nil
		}
	}
	for index := range children {
		if err := require(index, "number"); err != nil {
			return "", err
		}
	}
	if contains([]string{">", ">=", "<", "<="}, tree.Op) {
		return "boolean", nil
	}
	return "number", nil
}
