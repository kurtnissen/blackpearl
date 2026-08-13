package platform

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.38.0"
)

// InitTelemetry configures a process tracer provider and returns its shutdown function.
func InitTelemetry(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	if serviceName == "" {
		return nil, fmt.Errorf("telemetry service name is required")
	}
	serviceResource, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}

	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(serviceResource)}
	if exporterConfigured() {
		exporter, exporterErr := otlptracehttp.New(ctx)
		if exporterErr != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", exporterErr)
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	} else {
		options = append(options, sdktrace.WithSampler(sdktrace.NeverSample()))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider.Shutdown, nil
}

func exporterConfigured() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}
