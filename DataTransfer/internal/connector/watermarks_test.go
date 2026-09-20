package connector

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"io"
	"log/slog"
	"testing"
)

func TestDynamicWatermarksAndInvalidUpdatePreservesPolicy(t *testing.T) {
	m, err := NewManager(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m.upstream = make(chan *dt.DeviceMessage, 100)
	if err = m.SetWatermarks(&dt.QueueWatermarks{Reminder: 20, Warning: 40, Error: 90, Recovery: 10}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		m.upstream <- &dt.DeviceMessage{}
	}
	m.observeBackpressure()
	if m.QueueLevel() != "reminder" || m.bpActive.Load() {
		t.Fatal("reminder prematurely enabled backpressure")
	}
	for i := 25; i < 45; i++ {
		m.upstream <- &dt.DeviceMessage{}
	}
	m.observeBackpressure()
	if m.QueueLevel() != "warning" || !m.bpActive.Load() {
		t.Fatal("configured warning not applied")
	}
	for i := 45; i < 95; i++ {
		m.upstream <- &dt.DeviceMessage{}
	}
	m.observeBackpressure()
	if m.QueueLevel() != "error" {
		t.Fatal("error watermark not observed")
	}
	if err = m.SetWatermarks(&dt.QueueWatermarks{Reminder: 90, Warning: 20, Error: 80, Recovery: 30}); err == nil || m.Watermarks().Warning != 40 {
		t.Fatal("invalid update replaced effective watermarks")
	}
	for len(m.upstream) > 10 {
		<-m.upstream
	}
	m.observeBackpressure()
	if m.bpActive.Load() {
		t.Fatal("configured recovery not applied")
	}
}
