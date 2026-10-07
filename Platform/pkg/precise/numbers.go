// Package precise contains the numeric semantics shared by stored rollups and rule execution.
package precise

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"strconv"

	"competition2026/product/platform/pkg/model"
)

type Aggregate struct {
	Count    int64       `json:"count"`
	Excluded int64       `json:"excluded"`
	Sum      json.Number `json:"sum"`
	Min      json.Number `json:"min"`
	Max      json.Number `json:"max"`
	Average  *float64    `json:"average"`
	Complete string      `json:"complete"`
}

var decimalNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE]([+-]?[0-9]+))?$`)

// Bound decimal expansion before big.Rat allocates powers of ten.
func Number(v any) (*big.Rat, bool) {
	var text string
	switch x := v.(type) {
	case json.Number:
		text = x.String()
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, false
		}
		text = strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		text = strconv.FormatFloat(float64(x), 'g', -1, 32)
	case int64:
		text = strconv.FormatInt(x, 10)
	case uint64:
		text = strconv.FormatUint(x, 10)
	case int:
		text = strconv.Itoa(x)
	default:
		return nil, false
	}
	if len(text) > 768 {
		return nil, false
	}
	parts := decimalNumber.FindStringSubmatch(text)
	if parts == nil {
		return nil, false
	}
	if parts[1] != "" {
		exponent, err := strconv.Atoi(parts[1])
		if err != nil || exponent < -308 || exponent > 308 {
			return nil, false
		}
	}
	r, ok := new(big.Rat).SetString(text)
	if ok && (r.Num().BitLen() > 4096 || r.Denom().BitLen() > 4096) {
		return nil, false
	}
	return r, ok
}
func ratNumber(n *big.Rat) json.Number {
	if n == nil {
		return "0"
	}
	if n.IsInt() {
		return json.Number(n.Num().String())
	}
	f, _ := n.Float64()
	return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
}
func AggregatePoints(points []model.Observation) Aggregate {
	a := Aggregate{Sum: "0", Min: "0", Max: "0", Complete: "unknown"}
	sum := new(big.Rat)
	var min, max *big.Rat
	for _, p := range points {
		n, ok := Number(p.Value)
		if p.Quality != "GOOD" || !ok {
			a.Excluded++
			continue
		}
		a.Count++
		sum.Add(sum, n)
		if min == nil || n.Cmp(min) < 0 {
			min = new(big.Rat).Set(n)
		}
		if max == nil || n.Cmp(max) > 0 {
			max = new(big.Rat).Set(n)
		}
	}
	a.Sum = ratNumber(sum)
	a.Min = ratNumber(min)
	a.Max = ratNumber(max)
	if a.Count > 0 {
		mean, _ := new(big.Rat).Quo(sum, new(big.Rat).SetInt64(a.Count)).Float64()
		a.Average = &mean
	}
	return a
}
func MergeAggregate(a, b Aggregate) Aggregate {
	sum, _ := Number(a.Sum)
	other, _ := Number(b.Sum)
	if sum == nil {
		sum = new(big.Rat)
	}
	if other != nil {
		sum.Add(sum, other)
	}
	a.Sum = ratNumber(sum)
	if b.Count > 0 {
		lo, _ := Number(a.Min)
		blo, _ := Number(b.Min)
		hi, _ := Number(a.Max)
		bhi, _ := Number(b.Max)
		if a.Count == 0 || lo.Cmp(blo) > 0 {
			a.Min = b.Min
		}
		if a.Count == 0 || hi.Cmp(bhi) < 0 {
			a.Max = b.Max
		}
	}
	a.Count += b.Count
	a.Excluded += b.Excluded
	if a.Count > 0 {
		f, _ := new(big.Rat).Quo(sum, new(big.Rat).SetInt64(a.Count)).Float64()
		a.Average = &f
	}
	return a
}
