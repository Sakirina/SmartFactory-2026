package engine

import (
	"context"

	"competition2026/product/platform/internal/rulecore"
)

// Expression is the one-shot adapter for control conditions and diagnostics.
// Published rule execution uses its previously compiled expression instructions.
func Expression(ctx context.Context, code string, vars map[string]any) (any, error) {
	tree, err := rulecore.CompileExpression(code)
	if err != nil {
		return nil, err
	}
	return rulecore.EvaluateExpression(ctx, tree, vars)
}
func truth(value any) bool { return rulecore.Truth(value) }
