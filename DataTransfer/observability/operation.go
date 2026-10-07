package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Identity struct {
	MessageID, CommandID, RequestID, ActorID                       string
	EntityID, EntityRevision, DraftID, DraftRevision, DefinitionID string
}

type identityKey struct{}

func (i Identity) attributes() []attribute.KeyValue {
	values := []struct{ key, value string }{
		{"smartfactory.message_id", i.MessageID}, {"smartfactory.command_id", i.CommandID}, {"smartfactory.request_id", i.RequestID},
		{"smartfactory.actor_id", i.ActorID}, {"smartfactory.entity_id", i.EntityID}, {"smartfactory.entity_revision", i.EntityRevision},
		{"smartfactory.draft_id", i.DraftID}, {"smartfactory.draft_revision", i.DraftRevision}, {"smartfactory.definition_id", i.DefinitionID},
	}
	result := make([]attribute.KeyValue, 0, len(values))
	for _, item := range values {
		if item.value != "" {
			result = append(result, attribute.String(item.key, item.value))
		}
	}
	return result
}

// WithIdentity merges known business identity into both context and the active span.
func WithIdentity(ctx context.Context, i Identity) context.Context {
	previous, _ := ctx.Value(identityKey{}).(Identity)
	current := []*string{&previous.MessageID, &previous.CommandID, &previous.RequestID, &previous.ActorID, &previous.EntityID, &previous.EntityRevision, &previous.DraftID, &previous.DraftRevision, &previous.DefinitionID}
	values := []string{i.MessageID, i.CommandID, i.RequestID, i.ActorID, i.EntityID, i.EntityRevision, i.DraftID, i.DraftRevision, i.DefinitionID}
	for index, value := range values {
		if value != "" {
			*current[index] = value
		}
	}
	trace.SpanFromContext(ctx).SetAttributes(previous.attributes()...)
	return context.WithValue(ctx, identityKey{}, previous)
}

// LogAttrs correlates normal slog records without exporting log bodies or business payloads.
func LogAttrs(ctx context.Context) []slog.Attr {
	i, _ := ctx.Value(identityKey{}).(Identity)
	result := make([]slog.Attr, 0, 11)
	for _, a := range i.attributes() {
		result = append(result, slog.String(string(a.Key), a.Value.AsString()))
	}
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		result = append(result, slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return result
}

func Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return propagator.Extract(ctx, carrier)
}
func Inject(ctx context.Context, carrier propagation.TextMapCarrier) { propagator.Inject(ctx, carrier) }
func ExtractHTTP(ctx context.Context, header http.Header) context.Context {
	if active := trace.SpanContextFromContext(ctx); active.IsValid() && !active.IsRemote() {
		return ctx
	}
	return Extract(ctx, propagation.HeaderCarrier(header))
}
func InjectHTTP(ctx context.Context, header http.Header) {
	Inject(ctx, propagation.HeaderCarrier(header))
}

func startOperation(ctx context.Context, tracer trace.Tracer, meter metric.Meter, operation string, identity Identity) (context.Context, func(error)) {
	// Callers use a fixed operation name; object IDs stay out of metric attributes.
	ctx, span := tracer.Start(ctx, operation)
	ctx = WithIdentity(ctx, identity)
	started := time.Now()
	count, countErr := meter.Int64Counter("smartfactory.operations", metric.WithDescription("Completed application operations"))
	duration, durationErr := meter.Float64Histogram("smartfactory.operation.duration", metric.WithUnit("s"), metric.WithDescription("Application operation duration"))
	var once sync.Once
	return ctx, func(err error) {
		once.Do(func() {
			outcome := "ok"
			if err != nil {
				outcome = "error"
				span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
				span.RecordError(errors.New("application operation failed"))
				span.SetStatus(codes.Error, "operation failed")
			} else {
				span.SetStatus(codes.Ok, "")
			}
			attributes := metric.WithAttributes(attribute.String("smartfactory.operation", operation), attribute.String("smartfactory.outcome", outcome))
			if countErr == nil {
				count.Add(ctx, 1, attributes)
			}
			if durationErr == nil {
				duration.Record(ctx, time.Since(started).Seconds(), attributes)
			}
			span.End()
		})
	}
}
