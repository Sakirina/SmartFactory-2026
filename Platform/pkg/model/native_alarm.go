package model

// NativeAlarmUpdate carries only lifecycle facts from the bound native alarm.
// SourceVersion is derived from native timestamps and lifecycle flags.
type NativeAlarmUpdate struct {
	SourceID       string `json:"source_id"`
	NativeID       string `json:"native_id"`
	SourceVersion  int64  `json:"source_version"`
	StartedMS      int64  `json:"started_ms"`
	AcknowledgedMS int64  `json:"acknowledged_ms"`
	ClearedMS      int64  `json:"cleared_ms"`
	Acknowledged   bool   `json:"acknowledged"`
	Cleared        bool   `json:"cleared"`
}
type NativeAlarmState struct {
	ID string `json:"id"`
	NativeAlarmUpdate
	ObservedMS int64 `json:"observed_ms"`
	Version    int64 `json:"version"`
}
type NativeAlarmSyncStatus struct {
	SourceID       string `json:"source_id"`
	Enabled        bool   `json:"enabled"`
	IntervalMS     int64  `json:"interval_ms"`
	BatchLimit     int    `json:"batch_limit"`
	TimeoutMS      int64  `json:"timeout_ms"`
	FailedMappings int    `json:"failed_mappings"`
	AttemptMS      int64  `json:"attempt_ms"`
	SuccessMS      int64  `json:"success_ms"`
	Failures       int64  `json:"failures"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
}

func NativeAlarmVersion(v NativeAlarmUpdate) int64 {
	stamp := max(v.StartedMS, v.AcknowledgedMS, v.ClearedMS)
	flags := int64(0)
	if v.Acknowledged {
		flags++
	}
	if v.Cleared {
		flags += 2
	}
	return stamp*4 + flags
}
