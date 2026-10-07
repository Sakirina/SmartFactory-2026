package model

// Task is a business identity projected together with River's durable state.
type Task struct {
	BusinessState     string   `json:"business_state,omitempty"`
	Phase             string   `json:"phase,omitempty"`
	ReplayID          string   `json:"replay_id,omitempty"`
	Superseded        bool     `json:"superseded"`
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	BusinessID        string   `json:"business_id"`
	BusinessVersion   int64    `json:"business_version"`
	OutboxID          string   `json:"outbox_id,omitempty"`
	RiverID           int64    `json:"river_id"`
	Queue             string   `json:"queue"`
	Resources         []string `json:"resources"`
	State             string   `json:"state"`
	Attempt           int      `json:"attempt"`
	MaxAttempts       int      `json:"max_attempts"`
	NextMS            int64    `json:"next_ms"`
	CreatedMS         int64    `json:"created_ms"`
	StartedMS         int64    `json:"started_ms,omitempty"`
	FinishedMS        int64    `json:"finished_ms,omitempty"`
	CancelRequestedMS int64    `json:"cancel_requested_ms,omitempty"`
	Error             string   `json:"error,omitempty"`
	Progress          float64  `json:"progress"`
	CursorMS          int64    `json:"cursor_ms,omitempty"`
	Version           string   `json:"version"`
	AllowedActions    []string `json:"allowed_actions"`
}

type TaskAction struct {
	ExpectedVersion string `json:"expected_version" minLength:"1"`
}

type QueueBudget struct {
	Queue       string `json:"queue"`
	Concurrency int    `json:"concurrency"`
	Batch       int    `json:"batch"`
	TimeoutMS   int64  `json:"timeout_ms"`
	IntervalMS  int64  `json:"interval_ms"`
	Pending     int64  `json:"pending"`
	Running     int64  `json:"running"`
	Failed      int64  `json:"failed"`
}
