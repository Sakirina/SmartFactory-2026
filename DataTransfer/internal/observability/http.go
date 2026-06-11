// Package observability 提供管理端点(/healthz、/readyz、/metrics)
// 与 Prometheus 文本渲染,覆盖 FR-S-030 要求的连接、吞吐、缓冲、背压、策略指标。
package observability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
	writeGauge(&b, "datatransfer_ready", boolFloat(snapshot.Ready))
	writeGauge(&b, "datatransfer_grpc_serving", boolFloat(snapshot.GRPCServing))
	writeGauge(&b, "datatransfer_mqtt_connected", boolFloat(snapshot.MQTTConnected))
	writeGauge(&b, "datatransfer_buffer_messages", float64(snapshot.BufferSize))
	writeGauge(&b, "datatransfer_buffer_usage_percent", snapshot.BufferUsagePercent)
	writeGauge(&b, "datatransfer_connected_devices", float64(snapshot.ConnectedDevices))
	writeGauge(&b, "datatransfer_active_connectors", float64(snapshot.ActiveConnectors))
	writeCounter(&b, "datatransfer_upstream_messages_total", float64(snapshot.UpstreamTotal))
	writeCounter(&b, "datatransfer_downstream_commands_total", float64(snapshot.DownstreamTotal))
	writeCounter(&b, "datatransfer_rejected_commands_total", float64(snapshot.RejectedCommandTotal))
	writeCounter(&b, "datatransfer_duplicate_commands_total", float64(snapshot.DuplicateCommandTotal))
	writeCounter(&b, "datatransfer_config_rejections_total", float64(snapshot.ConfigRejectTotal))
	writeCounter(&b, "datatransfer_discovery_events_total", float64(snapshot.DiscoveryEventTotal))
	writeCounter(&b, "datatransfer_subscriber_dropped_total", float64(snapshot.SubscriberDropTotal))
	writeGauge(&b, "datatransfer_persistent_buffer_pending", float64(snapshot.PersistentBuffer.Pending))
	writeGauge(&b, "datatransfer_persistent_buffer_sending", float64(snapshot.PersistentBuffer.Sending))
	writeGauge(&b, "datatransfer_persistent_buffer_completed", float64(snapshot.PersistentBuffer.Completed))
	writeCounter(&b, "datatransfer_persistent_buffer_dropped_total", float64(snapshot.PersistentBuffer.Dropped))
	writeCounter(&b, "datatransfer_persistent_buffer_retry_total", float64(snapshot.PersistentBuffer.Retry))
	writeCounter(&b, "datatransfer_persistent_buffer_last_error_total", float64(snapshot.PersistentBuffer.LastErrorCount))
	writeGauge(&b, "datatransfer_persistent_buffer_capacity_bytes", float64(snapshot.PersistentBuffer.CapacityBytes))
	writeGauge(&b, "datatransfer_persistent_buffer_used_bytes", float64(snapshot.PersistentBuffer.UsedBytes))
	writeGauge(&b, "datatransfer_persistent_buffer_usage_percent", snapshot.PersistentBuffer.UsagePercent)
	writeCounter(&b, "datatransfer_replay_batches_total", float64(snapshot.PersistentBuffer.ReplayBatchTotal))

	// 背压观测(FR-S-039:触发与解除须对外可见)
	writeGauge(&b, "datatransfer_backpressure_active", boolFloat(snapshot.BackpressureActive))
	writeGauge(&b, "datatransfer_backpressure_queue_usage_percent", snapshot.QueueUsagePercent)
	writeCounter(&b, "datatransfer_backpressure_triggered_total", float64(snapshot.BackpressureTriggerTotal))
	writeCounter(&b, "datatransfer_backpressure_dropped_total", float64(snapshot.BackpressureDropTotal))

	// 上报策略统计(FR-S-030:被过滤消息数与实际上报率)
	writeCounter(&b, "datatransfer_strategy_filtered_messages_total", float64(snapshot.StrategyFilteredMessages))
	writeCounter(&b, "datatransfer_strategy_filtered_datapoints_total", float64(snapshot.StrategyFilteredPoints))
	writeCounter(&b, "datatransfer_strategy_delivered_messages_total", float64(snapshot.StrategyDeliveredCount))

	// 各 Connector 指标(FR-S-030:Connector 状态、设备数、错误数对外可见)
	renderConnectors(&b, snapshot)
	return b.String()
}

func renderConnectors(b *strings.Builder, snapshot dtruntime.Snapshot) {
	if len(snapshot.Connectors) == 0 {
		return
	}
	ids := make([]string, 0, len(snapshot.Connectors))
	for id := range snapshot.Connectors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Fprintf(b, "# TYPE datatransfer_connector_up gauge\n")
	fmt.Fprintf(b, "# TYPE datatransfer_connector_devices gauge\n")
	fmt.Fprintf(b, "# TYPE datatransfer_connector_errors_total counter\n")
	for _, id := range ids {
		metrics := snapshot.Connectors[id]
		labels := fmt.Sprintf(`connector_id="%s",protocol="%s",state="%s"`,
			escapeLabel(metrics.GetConnectorId()), escapeLabel(metrics.GetProtocol()), escapeLabel(metrics.GetState()))
		up := 0.0
		if metrics.GetState() == "running" {
			up = 1.0
		}
		fmt.Fprintf(b, "datatransfer_connector_up{%s} %g\n", labels, up)
		fmt.Fprintf(b, "datatransfer_connector_devices{%s} %d\n", labels, metrics.GetDeviceCount())
		fmt.Fprintf(b, "datatransfer_connector_errors_total{%s} %d\n", labels, metrics.GetErrorCount())
	}
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func writeGauge(b *strings.Builder, name string, value float64) {
	_, _ = fmt.Fprintf(b, "# TYPE %s gauge\n%s %g\n", name, name, value)
}

func writeCounter(b *strings.Builder, name string, value float64) {
	_, _ = fmt.Fprintf(b, "# TYPE %s counter\n%s %g\n", name, name, value)
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
