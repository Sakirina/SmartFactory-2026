package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExpressionNumericBudgetAndExactRounding(t *testing.T) {
	for _, code := range []string{"1e999999999", "1e-999999999", "1 && true", "choose(1,2,3)", strings.Repeat("1e308 * ", 5) + "1e308"} {
		if _, e := Expression(context.Background(), code, nil); e == nil {
			t.Fatalf("accepted %s", code)
		}
	}
	result, e := Expression(context.Background(), "round(value)", map[string]any{"value": json.Number("9007199254740993.5")})
	if e != nil || result != json.Number("9007199254740994") {
		t.Fatalf("round = %v %v", result, e)
	}
}
