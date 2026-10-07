package tasks

import (
	"context"
	"testing"
	"time"

	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTasksPreserveTraceIdentityAndBoundedMetricLabels(t *testing.T) {
	previousTrace, previousMetric := otel.GetTracerProvider(), otel.GetMeterProvider()
	recorder := tracetest.NewSpanRecorder()
	traceProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	metricProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetTracerProvider(traceProvider)
	otel.SetMeterProvider(metricProvider)
	defer func() {
		otel.SetTracerProvider(previousTrace)
		otel.SetMeterProvider(previousMetric)
		_ = traceProvider.Shutdown(context.Background())
		_ = metricProvider.Shutdown(context.Background())
	}()
	s := fixture(t, "")
	policy := s.Policy()
	policy.Archive.Enabled = true
	s.SetPolicy(policy)
	ctx, source := otel.Tracer("task-test").Start(context.Background(), "fixture.request")
	traceID := source.SpanContext().TraceID()
	ctx = observability.WithIdentity(ctx, observability.Identity{ActorID: "trace-operator", RequestID: "trace-request"})
	if _, err := s.Ingest(ctx, store.IngestBatch{MessageID: "trace-sample", SourceID: "collector", Points: []model.Observation{{DeviceID: "trace-device", Key: "temperature", Value: 5, ObservedMS: s.Now().Add(-48 * time.Hour).UnixMilli(), Quality: "GOOD"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *store.Tx) error {
		if err := tx.Enqueue("trace-delivery", "tb_entity", "trace-device", model.Entity{ID: "trace-device", Version: 1}); err != nil {
			return err
		}
		_, err := tx.EnqueueTask("archive", "trace-archive", 0, "", []string{"*"}, time.Time{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	source.End()
	service := start(t, s, Handlers{Projection: func(ctx context.Context, delivery store.Delivery) error {
		_, err := s.Put(ctx, "trace-delivery", delivery.ID, -1, map[string]string{"id": delivery.ID})
		return err
	}})
	until(t, 5*time.Second, func() bool {
		first, err := s.Task(context.Background(), "tb_entity:trace-delivery")
		second, err2 := s.Task(context.Background(), "archive:trace-archive")
		return err == nil && err2 == nil && first.State == "completed" && second.State == "completed"
	})
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	operations, linkedTransaction, taskIdentity := map[string]bool{}, false, false
	var execution sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.SpanContext().TraceID() != traceID {
			continue
		}
		operations[span.Name()] = true
		if span.Name() != "tasks.execute" {
			continue
		}
		attributes := map[string]string{}
		for _, attr := range span.Attributes() {
			attributes[string(attr.Key)] = attr.Value.AsString()
		}
		if attributes["smartfactory.task_id"] == "tb_entity:trace-delivery" && attributes["smartfactory.business_id"] == "trace-delivery" {
			execution, taskIdentity = span, true
		}
	}
	if execution != nil {
		for _, span := range recorder.Ended() {
			if span.Name() == "store.transaction" && span.Parent().SpanID() == execution.SpanContext().SpanID() {
				linkedTransaction = true
			}
		}
	}
	for _, name := range []string{"tasks.wait", "tasks.execute", "archive.prepare", "archive.commit", "store.transaction"} {
		if !operations[name] {
			t.Errorf("missing traced operation %s", name)
		}
	}
	if !taskIdentity || !linkedTransaction {
		t.Fatalf("task identity=%v transaction parent=%v", taskIdentity, linkedTransaction)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	foundWait := false
	for _, scope := range metrics.ScopeMetrics {
		for _, measure := range scope.Metrics {
			if measure.Name != "smartfactory.task.wait" {
				continue
			}
			foundWait = true
			for _, point := range measure.Data.(metricdata.Histogram[float64]).DataPoints {
				for _, attr := range point.Attributes.ToSlice() {
					if attr.Key != "smartfactory.queue" {
						t.Errorf("unbounded wait metric label: %s", attr.Key)
					}
				}
			}
		}
	}
	if !foundWait {
		t.Fatal("queue wait histogram missing")
	}
	t.Log("durable enqueue trace -> tasks.execute/tasks.wait -> archive.prepare/archive.commit/store.transaction; task and business IDs in spans; queue-only wait metric")
}
