package acceptance

import "testing"

func TestSequenceRecorderDetectsReorderDuplicatesGapsAndUnexpectedValues(t *testing.T) {
	r, err := newSequenceRecorder(2048)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1000; i >= 1; i-- {
		if i == 513 {
			continue
		}
		fresh, err := r.add("modbus-001", uint64(i))
		if err != nil || !fresh {
			t.Fatalf("sample %d: %v %v", i, fresh, err)
		}
	}
	if fresh, err := r.add("modbus-001", 1000); err != nil || fresh {
		t.Fatal("duplicate was accepted", fresh, err)
	}
	_, counts, valid := r.evidence(map[string]uint64{"modbus-001": 1000})
	if valid || counts["modbus"] != 999 {
		t.Fatal("missing sample was not detected", counts, valid)
	}
	if _, err = r.add("modbus-001", 513); err != nil {
		t.Fatal(err)
	}
	_, counts, valid = r.evidence(map[string]uint64{"modbus-001": 1000})
	if !valid || counts["modbus"] != 1000 {
		t.Fatal("exact reordered set was rejected", counts, valid)
	}
	if _, err = r.add("modbus-001", 1001); err != nil {
		t.Fatal(err)
	}
	_, _, valid = r.evidence(map[string]uint64{"modbus-001": 1000})
	if valid {
		t.Fatal("unexpected sequence was not detected")
	}
	for _, id := range []string{"unknown", "modbus-201"} {
		if _, err = r.add(id, 1); err == nil {
			t.Fatal("unknown device accepted", id)
		}
	}
	if _, err = r.add("modbus-001", 1<<32); err == nil {
		t.Fatal("unbounded allocation accepted")
	}
}

func TestSequenceRecorderMemoryBudget(t *testing.T) {
	r, err := newSequenceRecorder(48000)
	if err != nil {
		t.Fatal(err)
	}
	if r.allocated > 4<<20 {
		t.Fatalf("60-minute recorder uses %d bytes", r.allocated)
	}
	if _, err = newSequenceRecorder(1 << 32); err == nil {
		t.Fatal("memory budget not enforced")
	}
}
