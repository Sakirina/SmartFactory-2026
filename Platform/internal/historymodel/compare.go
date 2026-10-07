package historymodel

import (
	"bytes"
	"competition2026/product/platform/pkg/precise"
	"encoding/json"
	"errors"
	"reflect"
)

var ErrParentPending = errors.New("previous shadow generation is not complete")

func different(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	if e != nil || f != nil {
		return true
	}
	var left, right any
	dx, dy := json.NewDecoder(bytes.NewReader(x)), json.NewDecoder(bytes.NewReader(y))
	dx.UseNumber()
	dy.UseNumber()
	if dx.Decode(&left) != nil || dy.Decode(&right) != nil {
		return true
	}
	return !equalValue(left, right)
}

func equalValue(a, b any) bool {
	if n, ok := precise.Number(a); ok {
		m, valid := precise.Number(b)
		return valid && n.Cmp(m) == 0
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !equalValue(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !equalValue(v, y[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// Compare ignores plan and version identities so equal behavior remains equal.
func Compare(a, b Lane) Difference {
	path := func(l Lane) []any {
		out := []any{l.Skipped}
		for _, n := range l.Evaluation.Trace {
			out = append(out, []any{n.NodeID, n.Type, n.Skipped, n.Error})
		}
		return out
	}
	x, y := a.Evaluation, b.Evaluation
	d := Difference{Values: different(x.Values, y.Values), Quality: different(x.Quality, y.Quality), Alarm: different([]any{x.Alarm, x.Severity}, []any{y.Alarm, y.Severity}), State: different(x.States, y.States), Path: different(path(a), path(b)), Trigger: x.Trigger != y.Trigger}
	d.Changed = d.Values || d.Quality || d.Alarm || d.State || d.Path || d.Trigger
	return d
}
