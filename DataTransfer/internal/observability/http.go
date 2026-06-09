package observability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	dtruntime "competition2026/product/datatransfer/internal/runtime"
)

func Handler(rt *dtruntime.Runtime) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if rt.Ready() {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(RenderPrometheus(rt.Snapshot())))
	})
	return mux
}

func RenderPrometheus(snapshot dtruntime.Snapshot) string {
	var b strings.Builder
	writeMetric(&b, "datatransfer_ready", boolFloat(snapshot.Ready))
	writeMetric(&b, "datatransfer_grpc_serving", boolFloat(snapshot.GRPCServing))
	writeMetric(&b, "datatransfer_mqtt_connected", boolFloat(snapshot.MQTTConnected))
	writeMetric(&b, "datatransfer_buffer_messages", float64(snapshot.BufferSize))
	writeMetric(&b, "datatransfer_buffer_usage_percent", snapshot.BufferUsagePercent)
	writeMetric(&b, "datatransfer_connected_devices", float64(snapshot.ConnectedDevices))
	writeMetric(&b, "datatransfer_active_connectors", float64(snapshot.ActiveConnectors))
	writeMetric(&b, "datatransfer_upstream_messages_total", float64(snapshot.UpstreamTotal))
	writeMetric(&b, "datatransfer_downstream_commands_total", float64(snapshot.DownstreamTotal))
	writeMetric(&b, "datatransfer_rejected_commands_total", float64(snapshot.RejectedCommandTotal))
	writeMetric(&b, "datatransfer_duplicate_commands_total", float64(snapshot.DuplicateCommandTotal))
	writeMetric(&b, "datatransfer_config_rejections_total", float64(snapshot.ConfigRejectTotal))
	writeMetric(&b, "datatransfer_discovery_events_total", float64(snapshot.DiscoveryEventTotal))
	writeMetric(&b, "datatransfer_persistent_buffer_pending", float64(snapshot.PersistentBuffer.Pending))
	writeMetric(&b, "datatransfer_persistent_buffer_sending", float64(snapshot.PersistentBuffer.Sending))
	writeMetric(&b, "datatransfer_persistent_buffer_completed", float64(snapshot.PersistentBuffer.Completed))
	writeMetric(&b, "datatransfer_persistent_buffer_dropped_total", float64(snapshot.PersistentBuffer.Dropped))
	writeMetric(&b, "datatransfer_persistent_buffer_retry_total", float64(snapshot.PersistentBuffer.Retry))
	writeMetric(&b, "datatransfer_persistent_buffer_last_error_total", float64(snapshot.PersistentBuffer.LastErrorCount))
	writeMetric(&b, "datatransfer_persistent_buffer_capacity_bytes", float64(snapshot.PersistentBuffer.CapacityBytes))
	writeMetric(&b, "datatransfer_persistent_buffer_used_bytes", float64(snapshot.PersistentBuffer.UsedBytes))
	writeMetric(&b, "datatransfer_persistent_buffer_usage_percent", snapshot.PersistentBuffer.UsagePercent)
	writeMetric(&b, "datatransfer_replay_batches_total", float64(snapshot.PersistentBuffer.ReplayBatchTotal))
	return b.String()
}

func writeMetric(b *strings.Builder, name string, value float64) {
	_, _ = fmt.Fprintf(b, "# TYPE %s gauge\n%s %g\n", name, name, value)
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
