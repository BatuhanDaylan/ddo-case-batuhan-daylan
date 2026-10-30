// Package telemetry OpenTelemetry iz (trace) altyapısını kurar ve HTTP isteklerini izler.
//
// Varsayılan olarak izler KAPALIDIR (global noop sağlayıcı: maliyeti yok). OTEL_TRACES_EXPORTER=stdout ile her iz
// standart çıktıya JSON olarak yazılır. Gelen W3C `traceparent` başlığı kabul edilir; böylece iz, çağıran sistemin
// izine bağlanır. Yanıtlarda X-Trace-Id başlığı iz kimliğini verir.
package telemetry

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Init izleme sağlayıcısını kurar ve kapatma işlevini döner. exporter: "stdout" | "" (kapalı).
func Init(exporter string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if exporter != "stdout" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := stdouttrace.New()
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "uninet-api"))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Middleware her HTTP isteği için sunucu span'i açar. Span adı yol DESENİ olduğundan ("POST /sections/{id}/enrollments")
// iz sayısı kimliklerle çoğalmaz.
func Middleware(next http.Handler) http.Handler {
	tracer := otel.Tracer("uninet/http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, "HTTP "+r.Method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		if sc := span.SpanContext(); sc.IsValid() {
			w.Header().Set("X-Trace-Id", sc.TraceID().String())
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(ctx))

		route := r.URL.Path
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(attribute.String("http.method", r.Method), attribute.String("http.route", route), attribute.Int("http.status_code", sw.status))
		if sw.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(sw.status))
		}
	})
}
