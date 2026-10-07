package model

import "encoding/json"

// QueryRequest describes a bounded page. The continuation retains the same
// committed snapshot and filters; cursor strings are opaque to the client.
type QueryRequest struct {
	ResourceIDs    []string `json:"resource_ids,omitempty"`
	Keys           []string `json:"keys,omitempty"`
	DefinitionID   string   `json:"definition_id,omitempty"`
	Status         string   `json:"status,omitempty"`
	EntityKind     string   `json:"entity_kind,omitempty"`
	Search         string   `json:"search,omitempty"`
	AssigneeID     string   `json:"assignee_id,omitempty"`
	HandlingStatus string   `json:"handling_status,omitempty"`
	Active         *bool    `json:"active,omitempty"`
	Acknowledged   *bool    `json:"acknowledged,omitempty"`
	FromMS         int64    `json:"from_ms,omitempty"`
	ToMS           int64    `json:"to_ms,omitempty"`
	Resolution     string   `json:"resolution,omitempty"`
	Limit          int      `json:"limit,omitempty" minimum:"1" maximum:"2000"`
	PageToken      string   `json:"page_token,omitempty"`
}

type QueryRow struct {
	Kind     string          `json:"kind"`
	ID       string          `json:"id"`
	Revision string          `json:"revision"`
	Version  int64           `json:"version"`
	SortMS   int64           `json:"sort_ms"`
	Data     json.RawMessage `json:"data"`
}

type QueryMetadata struct {
	Quality      QualitySummary `json:"quality"`
	QualityScope string         `json:"quality_scope"`
	Sources      []SourceState  `json:"sources"`
	Revisions    []Revision     `json:"revisions"`
	Gaps         []DataGap      `json:"gaps"`
}

type QueryPage struct {
	Items          []QueryRow    `json:"items"`
	SnapshotCursor string        `json:"snapshot_cursor"`
	NextPageToken  string        `json:"next_page_token,omitempty"`
	HasMore        bool          `json:"has_more"`
	Scope          QueryRequest  `json:"scope"`
	Metadata       QueryMetadata `json:"metadata"`
}

type QueryChange struct {
	Operation string    `json:"operation" enum:"upsert,remove"`
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Revision  string    `json:"revision"`
	Item      *QueryRow `json:"item,omitempty"`
}

type QueryEvent struct {
	Type      string         `json:"type" enum:"snapshot,delta,checkpoint,reset"`
	Cursor    string         `json:"cursor,omitempty"`
	Page      *QueryPage     `json:"page,omitempty"`
	Changes   []QueryChange  `json:"changes,omitempty"`
	HasMore   bool           `json:"has_more"`
	Metadata  *QueryMetadata `json:"metadata,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Clear     bool           `json:"clear"`
	Retryable bool           `json:"retryable"`
}
