package store

import (
	"competition2026/product/platform/pkg/model"
	"context"
)

// Budgets are per application process. PostgreSQL retains twelve shared
// connections; River work runs outside acquisition transactions. SQLite keeps
// one database connection, while compression and external I/O run without it.
func WorkBudgets() []model.QueueBudget {
	return []model.QueueBudget{
		{Queue: "ingest", Concurrency: 4, Batch: 10000, TimeoutMS: 30000},
		{Queue: "control", Concurrency: 3, Batch: 20, IntervalMS: 100},
		{Queue: "analysis", Concurrency: 1, Batch: 256, IntervalMS: 50},
		{Queue: "strategy", Concurrency: 1, Batch: 256, IntervalMS: 20},
		{Queue: "history_analysis", Concurrency: 1, Batch: 10000, TimeoutMS: 30000, IntervalMS: 100},
		{Queue: "recompute", Concurrency: 1, Batch: 40000, TimeoutMS: 30000, IntervalMS: 100},
		{Queue: "archive", Concurrency: 1, Batch: 4096, TimeoutMS: 5000, IntervalMS: 5000},
		{Queue: "projection", Concurrency: 2, Batch: 1, TimeoutMS: 15000, IntervalMS: 100},
		{Queue: "maintenance", Concurrency: 1, Batch: 1, TimeoutMS: 5000, IntervalMS: 5000},
	}
}
func (s *Store) QueueBudgets(ctx context.Context) ([]model.QueueBudget, error) {
	result := WorkBudgets()
	result[0].Running = int64(len(s.ingestSlots))
	result[1].Running = int64(len(s.controlSlots))
	rows, err := s.DB.QueryContext(ctx, "SELECT queue,state,COUNT(*) FROM river_job GROUP BY queue,state")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var queue, state string
		var n int64
		if err = rows.Scan(&queue, &state, &n); err != nil {
			rows.Close()
			return nil, err
		}
		for i := range result {
			if result[i].Queue != queue {
				continue
			}
			switch state {
			case "running":
				result[i].Running += n
			case "available", "scheduled", "retryable", "pending":
				result[i].Pending += n
			case "discarded":
				result[i].Failed += n
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.DB.QueryContext(ctx, "SELECT kind,COUNT(*) FROM outbox WHERE kind IN ('engine','strategy','edge_downlink','edge_control_operation','strategy_trigger','scheduled_execution') GROUP BY kind")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int64
		if err = rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		queue := "control"
		if kind == "engine" {
			queue = "analysis"
		} else if kind == "strategy" {
			queue = "strategy"
		}
		for i := range result {
			if result[i].Queue == queue {
				result[i].Pending += n
			}
		}
	}
	return result, rows.Err()
}

// AcquireControl covers the full Run call, including remote step waits. Waiting
// consumes no database connection and is interrupted by the caller's context.
func (s *Store) AcquireControl(ctx context.Context) (func(), error) {
	select {
	case s.controlSlots <- struct{}{}:
		return func() { <-s.controlSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
