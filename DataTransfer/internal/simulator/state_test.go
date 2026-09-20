package simulator

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestPulseCommitReplayAndDeviceBoundAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, e := OpenState(path)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		payload, e := s.NextPulse()
		if e != nil {
			t.Fatal(e)
		}
		if payload["data"].(map[string]string)["total"] != fmt.Sprint(i+1) {
			t.Fatal("event and device cumulative count diverged")
		}
	}
	before, e := s.Pending(1000)
	if e != nil || len(before) != 100 {
		t.Fatal(e, len(before))
	}
	s.DB.Close()
	s, e = OpenState(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	after, e := s.Pending(1000)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("replayed payload changed after restart")
	}
	if e = s.Acknowledge("light-1", before[0].ID); e != nil {
		t.Fatal(e)
	}
	after, _ = s.Pending(1000)
	if len(after) != 100 {
		t.Fatal("other device removed event")
	}
	if e = s.Acknowledge("counter-1", before[0].ID); e != nil {
		t.Fatal(e)
	}
	after, _ = s.Pending(1000)
	if len(after) != 99 {
		t.Fatal("ack did not retire event")
	}
	if s.Snapshot()["counter-1"]["total"] != float64(100) {
		t.Fatal("counter did not persist")
	}
}
func TestStorageFailureRollsBackSimulatorState(t *testing.T) {
	s, e := OpenState(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if _, e = s.DB.Exec("CREATE TRIGGER fail_state BEFORE UPDATE ON state BEGIN SELECT RAISE(FAIL, 'injected disk failure'); END"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.NextPulse(); e == nil {
		t.Fatal("accepted event despite failed state commit")
	}
	pending, _ := s.Pending(1000)
	if len(pending) != 0 || s.Snapshot()["counter-1"]["total"] != float64(0) {
		t.Fatal("partial event commit")
	}
	if e = s.Set("light-1", "light", true); e == nil || s.Snapshot()["light-1"]["light"] != false {
		t.Fatal("failed set changed device")
	}
	if _, e = s.Execute("light-1", Command{ID: "failure", Action: "set_light", Params: map[string]string{"value": "true"}}); e == nil || s.Snapshot()["light-1"]["light"] != false {
		t.Fatal("failed command changed device")
	}
}
