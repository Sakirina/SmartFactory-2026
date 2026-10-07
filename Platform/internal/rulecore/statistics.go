package rulecore

import "sync/atomic"

var compilations atomic.Uint64
var expressionParses atomic.Uint64
var executions atomic.Uint64

// Statistics exposes monotonic process counters so tests and diagnostics can
// measure parser reuse across real publication, restart and execution paths.
type Statistics struct {
	Compilations     uint64 `json:"compilations"`
	ExpressionParses uint64 `json:"expression_parses"`
	Executions       uint64 `json:"executions"`
}

func SnapshotStatistics() Statistics {
	return Statistics{compilations.Load(), expressionParses.Load(), executions.Load()}
}
