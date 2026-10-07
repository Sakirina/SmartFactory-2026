// Package observability provides the shared OpenTelemetry runtime for SmartFactory services.
package observability

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config selects providers without changing business transport or identity fields.
type Config struct {
	ServiceName, ServiceVersion, NodeID, Environment string
	Disabled                                         bool
	TraceExporter, MetricExporter                    string
	Endpoint                                         string
	Sampler                                          string
	SampleRatio                                      float64
	ExportTimeout, MetricInterval                    time.Duration
}

// ConfigFromEnv uses the OTEL environment variables and SmartFactory node identity.
// With no exporter configured, telemetry stays in the process and makes no network requests.
func ConfigFromEnv(serviceName string) (Config, error) {
	c := Config{
		ServiceName:    env("OTEL_SERVICE_NAME", serviceName),
		ServiceVersion: os.Getenv("SF_SERVICE_VERSION"), NodeID: os.Getenv("SF_NODE_ID"),
		Environment:   env("SF_ENVIRONMENT", "development"),
		TraceExporter: env("OTEL_TRACES_EXPORTER", "none"), MetricExporter: env("OTEL_METRICS_EXPORTER", "none"),
		Endpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), SampleRatio: 1, Sampler: "parentbased_always_on",
		ExportTimeout: 5 * time.Second, MetricInterval: 30 * time.Second,
	}
	if value := os.Getenv("OTEL_SDK_DISABLED"); value != "" {
		var err error
		c.Disabled, err = strconv.ParseBool(value)
		if err != nil {
			return c, fmt.Errorf("OTEL_SDK_DISABLED: %w", err)
		}
	}
	if sampler := os.Getenv("OTEL_TRACES_SAMPLER"); sampler != "" {
		c.Sampler = sampler
		switch sampler {
		case "parentbased_always_on", "always_on":
			c.SampleRatio = 1
		case "parentbased_always_off", "always_off":
			c.SampleRatio = 0
		case "parentbased_traceidratio", "traceidratio":
			value := env("OTEL_TRACES_SAMPLER_ARG", "1")
			var err error
			c.SampleRatio, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return c, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG: %w", err)
			}
		default:
			return c, fmt.Errorf("unsupported OTEL_TRACES_SAMPLER %q", sampler)
		}
	}
	for _, item := range []struct {
		name        string
		destination *time.Duration
	}{
		{"OTEL_EXPORTER_OTLP_TIMEOUT", &c.ExportTimeout}, {"OTEL_METRIC_EXPORT_INTERVAL", &c.MetricInterval},
	} {
		if value := os.Getenv(item.name); value != "" {
			milliseconds, err := strconv.ParseInt(value, 10, 64)
			if err != nil || milliseconds <= 0 || milliseconds > int64(time.Duration(1<<63-1)/time.Millisecond) {
				return c, fmt.Errorf("%s must be positive milliseconds", item.name)
			}
			*item.destination = time.Duration(milliseconds) * time.Millisecond
		}
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ServiceName) == "" {
		return errors.New("telemetry service name is required")
	}
	if math.IsNaN(c.SampleRatio) || math.IsInf(c.SampleRatio, 0) || c.SampleRatio < 0 || c.SampleRatio > 1 {
		return errors.New("telemetry sample ratio must be between 0 and 1")
	}
	switch c.Sampler {
	case "", "always_on", "always_off", "traceidratio", "parentbased_always_on", "parentbased_always_off", "parentbased_traceidratio":
	default:
		return fmt.Errorf("unsupported telemetry sampler %q", c.Sampler)
	}
	for _, exporter := range []string{c.TraceExporter, c.MetricExporter} {
		if exporter != "" && exporter != "none" && exporter != "otlp" {
			return fmt.Errorf("unsupported telemetry exporter %q", exporter)
		}
	}
	if !c.Disabled && (c.TraceExporter == "otlp" || c.MetricExporter == "otlp") {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("OTLP endpoint must be an HTTP(S) URL without credentials, query or fragment")
		}
	}
	if c.ExportTimeout < 0 || c.MetricInterval < 0 {
		return errors.New("telemetry intervals must not be negative")
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
