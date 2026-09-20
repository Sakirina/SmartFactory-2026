package acceptance

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/bits"
	"sort"
	"strings"
	"sync"
)

// sequenceRecorder verifies every source sequence with one bit per value. Its
// memory budget is fixed before sampling; input cannot trigger an unbounded map.
type sequenceRecorder struct {
	mu        sync.Mutex
	series    map[string][]uint64
	maximum   uint64
	allocated int64
}

type sequenceEvidence struct {
	DeviceID     string `json:"device_id"`
	Expected     uint64 `json:"expected"`
	Received     uint64 `json:"received"`
	Missing      uint64 `json:"missing"`
	Unexpected   uint64 `json:"unexpected"`
	BitmapSHA256 string `json:"bitmap_sha256"`
}

func newSequenceRecorder(maximum uint64) (*sequenceRecorder, error) {
	words := (maximum + 63) / 64
	if maximum == 0 || maximum > 1<<32 || words*8*500 > 64<<20 {
		return nil, fmt.Errorf("sequence recorder exceeds its 64 MiB budget; reduce duration or run separate intervals")
	}
	r := &sequenceRecorder{series: map[string][]uint64{}, maximum: maximum}
	for protocol, count := range map[string]int{"modbus": 200, "mqtt": 200, "opcua": 100} {
		for i := 0; i < count; i++ {
			id := i + 1
			if protocol == "opcua" {
				id = i
			}
			r.series[fmt.Sprintf("%s-%03d", protocol, id)] = make([]uint64, words)
			r.allocated += int64(words * 8)
		}
	}
	return r, nil
}

func (r *sequenceRecorder) add(device string, sequence uint64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values, ok := r.series[device]
	if !ok || sequence == 0 || sequence > r.maximum {
		return false, fmt.Errorf("unexpected source sequence %s:%d", device, sequence)
	}
	index, mask := (sequence-1)/64, uint64(1)<<((sequence-1)%64)
	if values[index]&mask != 0 {
		return false, nil
	}
	values[index] |= mask
	return true, nil
}

func (r *sequenceRecorder) evidence(expected map[string]uint64) ([]sequenceEvidence, map[string]int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]sequenceEvidence, 0, len(r.series))
	counts := map[string]int64{}
	valid := true
	for device, values := range r.series {
		item := sequenceEvidence{DeviceID: device, Expected: expected[device]}
		digest := sha256.New()
		var bytes [8]byte
		for i, word := range values {
			item.Received += uint64(bits.OnesCount64(word))
			binary.LittleEndian.PutUint64(bytes[:], word)
			digest.Write(bytes[:])
			start := uint64(i) * 64
			if start >= item.Expected {
				item.Unexpected += uint64(bits.OnesCount64(word))
			} else if remaining := item.Expected - start; remaining < 64 {
				item.Unexpected += uint64(bits.OnesCount64(word >> remaining))
			}
		}
		item.Missing = item.Expected - (item.Received - item.Unexpected)
		item.BitmapSHA256 = fmt.Sprintf("%x", digest.Sum(nil))
		valid = valid && item.Missing == 0 && item.Unexpected == 0
		counts[strings.SplitN(device, "-", 2)[0]] += int64(item.Received)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].DeviceID < result[j].DeviceID })
	return result, counts, valid
}
