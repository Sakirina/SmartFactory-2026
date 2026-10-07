package observability

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"regexp"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// HTTPContext preserves ResponseWriter interfaces while tracking requests and rejections.
func HTTPContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := ExtractHTTP(r.Context(), r.Header)
		requestID := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(requestID) {
			requestID = "request-" + rand.Text()
		}
		ctx, finish := StartOperation(ctx, "http.request", Identity{RequestID: requestID})
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("http.request.method", r.Method))
		defer func() {
			if value := recover(); value != nil {
				finish(errors.New("HTTP handler panicked"))
				panic(value)
			}
		}()
		request := r.WithContext(ctx)
		request.Header = r.Header.Clone()
		request.Header.Set("X-Request-ID", requestID)
		w.Header().Set("X-Request-ID", requestID)
		result := httpsnoop.CaptureMetrics(next, w, request)
		span.SetAttributes(attribute.Int("http.response.status_code", result.Code))
		if result.Code >= 400 {
			finish(errors.New("HTTP request rejected"))
		} else {
			finish(nil)
		}
	})
}

func messageIdentity(message any) Identity {
	var result Identity
	if value, ok := message.(interface{ GetMessageId() string }); ok {
		result.MessageID = value.GetMessageId()
	}
	if value, ok := message.(interface{ GetCommandId() string }); ok {
		result.CommandID = value.GetCommandId()
	}
	return result
}

func metadataCarrier(md metadata.MD) propagation.MapCarrier {
	carrier := propagation.MapCarrier{}
	for key, values := range md {
		if len(values) > 0 {
			carrier[key] = values[0]
		}
	}
	return carrier
}

// UnaryServerInterceptor extracts W3C context and correlates protobuf business IDs.
func UnaryServerInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	ctx = Extract(ctx, metadataCarrier(md))
	ctx, finish := StartOperation(ctx, info.FullMethod, messageIdentity(req))
	response, err := handler(ctx, req)
	finish(err)
	return response, err
}

// UnaryClientInterceptor propagates the active trace while retaining existing metadata.
func UnaryClientInterceptor(ctx context.Context, method string, req, reply any, connection *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
	ctx, finish := StartOperation(ctx, method, messageIdentity(req))
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	carrier := propagation.MapCarrier{}
	Inject(ctx, carrier)
	for key, value := range carrier {
		md.Set(key, value)
	}
	err := invoker(metadata.NewOutgoingContext(ctx, md), method, req, reply, connection, options...)
	finish(err)
	return err
}
