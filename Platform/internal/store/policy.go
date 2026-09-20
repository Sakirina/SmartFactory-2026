package store

import "time"

type RuntimePolicy struct {
	Confirmations                                                                 Confirmations
	Retention                                                                     Retention
	Archive                                                                       ArchivePolicy
	AutoBackfillDays                                                              int64
	HeartbeatMS, OfflineMS, ApprovalTTLMS, StartTTLMS, PermissionTTLMS, RefreshMS int64
	Reminder, Warning, Error, Recovery                                            int64
	QueueCapacity                                                                 int64
}
type Confirmations struct {
	Engineers      int `json:"engineers"`
	Leaders        int `json:"leaders"`
	ForceEngineers int `json:"force_engineers"`
	ForceLeaders   int `json:"force_leaders"`
}

func DefaultPolicy() RuntimePolicy {
	return RuntimePolicy{Confirmations: Confirmations{Engineers: 1, Leaders: 1, ForceEngineers: 2, ForceLeaders: 1}, Retention: DefaultRetention(), Archive: DefaultArchivePolicy(), AutoBackfillDays: 30, HeartbeatMS: 5000, OfflineMS: 15000, ApprovalTTLMS: 300000, StartTTLMS: 10000, PermissionTTLMS: 172800000, RefreshMS: 1000, Reminder: 60, Warning: 80, Error: 95, Recovery: 50, QueueCapacity: 200000}
}
func (s *Store) Policy() RuntimePolicy {
	if policy := s.policy.Load(); policy != nil {
		return *policy
	}
	return DefaultPolicy()
}
func (s *Store) SetPolicy(p RuntimePolicy) { s.policy.Store(&p) }
func Milliseconds(ms int64) time.Duration  { return time.Duration(ms) * time.Millisecond }
