package runtime

import (
	"sync"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
)

type ringStore struct {
	mu       sync.RWMutex
	messages []*dtv1.DeviceMessage
	capacity int
	next     int
	full     bool
}

func newRingStore(capacity int) *ringStore {
	if capacity <= 0 {
		capacity = 1024
	}
	return &ringStore{
		messages: make([]*dtv1.DeviceMessage, capacity),
		capacity: capacity,
	}
}

func (s *ringStore) Add(msg *dtv1.DeviceMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages[s.next] = msg
	s.next = (s.next + 1) % s.capacity
	if s.next == 0 {
		s.full = true
	}
}

func (s *ringStore) Since(sinceTimestamp int64, maxCount int, filter Filter) []*dtv1.DeviceMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if maxCount <= 0 {
		maxCount = 100
	}
	ordered := s.orderedLocked()
	out := make([]*dtv1.DeviceMessage, 0, min(maxCount, len(ordered)))
	for _, msg := range ordered {
		if msg.GetTimestamp() < sinceTimestamp {
			continue
		}
		if !filter.Match(msg) {
			continue
		}
		out = append(out, msg)
		if len(out) >= maxCount {
			break
		}
	}
	return out
}

func (s *ringStore) Stats() (int, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	size := s.sizeLocked()
	return size, float64(size) / float64(s.capacity) * 100
}

func (s *ringStore) orderedLocked() []*dtv1.DeviceMessage {
	size := s.sizeLocked()
	if size == 0 {
		return nil
	}
	out := make([]*dtv1.DeviceMessage, 0, size)
	if !s.full {
		for i := 0; i < s.next; i++ {
			if s.messages[i] != nil {
				out = append(out, s.messages[i])
			}
		}
		return out
	}
	for i := s.next; i < s.capacity; i++ {
		if s.messages[i] != nil {
			out = append(out, s.messages[i])
		}
	}
	for i := 0; i < s.next; i++ {
		if s.messages[i] != nil {
			out = append(out, s.messages[i])
		}
	}
	return out
}

func (s *ringStore) sizeLocked() int {
	if s.full {
		return s.capacity
	}
	return s.next
}
