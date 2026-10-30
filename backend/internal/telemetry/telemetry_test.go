package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func setup(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	if _, err := Init(""); err != nil { // yayıcıyı (propagator) kurar; dışa aktarım kapalı
		t.Fatal(err)
	}
	sr := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return sr
}

// Gelen W3C traceparent başlığı kabul edilir: sunucu span'i çağıranın iz kimliğiyle açılır ve
// iz kimliği X-Trace-Id olarak yanıtta döner.
func TestMiddlewareContinuesIncomingTraceAndNamesSpanByRoute(t *testing.T) {
	sr := setup(t)
	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/sections/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	req := httptest.NewRequest("GET", "/sections/42", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Trace-Id"); got != traceID {
		t.Errorf("X-Trace-Id %q, beklenen %q", got, traceID)
	}
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("span sayısı %d, beklenen 1", len(spans))
	}
	s := spans[0]
	if s.SpanContext().TraceID().String() != traceID {
		t.Errorf("iz kimliği çağıranınkiyle aynı olmalı: %s", s.SpanContext().TraceID())
	}
	if s.Name() != "GET /sections/{id}" { // kimlik değil, yol DESENİ (kardinalite)
		t.Errorf("span adı %q", s.Name())
	}
	attrs := map[string]any{}
	for _, a := range s.Attributes() {
		attrs[string(a.Key)] = a.Value.AsInterface()
	}
	if attrs["http.status_code"] != int64(http.StatusTeapot) || attrs["http.route"] != "/sections/{id}" {
		t.Errorf("öznitelikler: %v", attrs)
	}
}

func TestMiddlewareMarksServerErrors(t *testing.T) {
	sr := setup(t)
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if s := sr.Ended(); len(s) != 1 || s[0].Status().Code.String() != "Error" {
		t.Errorf("5xx span'i Error durumunda olmalı: %+v", s)
	}
}

func TestInitStdoutAndNoop(t *testing.T) {
	shutdown, err := Init("")
	if err != nil || shutdown == nil {
		t.Fatal("kapalı mod hata verdi")
	}
	if err := shutdown(t.Context()); err != nil {
		t.Error(err)
	}
	prev := otel.GetTracerProvider()
	defer otel.SetTracerProvider(prev)
	shutdown, err = Init("stdout")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(t.Context()); err != nil {
		t.Error(err)
	}
}
