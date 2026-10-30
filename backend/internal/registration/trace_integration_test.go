package registration

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Kritik akış (kayıt) izlenir: kök span + her aşama için alt span, hepsi aynı iz kimliğinde.
func TestEnrollFlowIsTraced(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	defer otel.SetTracerProvider(prev)

	pool := newTestPool(t)
	sectionID, ids := fixture(t, pool, 3, 1) // kontenjan 1: ikinci öğrenci bekleme listesine girer
	ctx := context.Background()

	if _, err := enrollOnce(ctx, pool, ids[0], sectionID); err != nil {
		t.Fatal(err)
	}
	first := sr.Ended()
	if _, err := enrollOnce(ctx, pool, ids[1], sectionID); err != nil {
		t.Fatal(err)
	}
	second := sr.Ended()[len(first):]

	check := func(label string, spans []sdktrace.ReadOnlySpan, outcome string) {
		var root sdktrace.ReadOnlySpan
		children := map[string]bool{}
		for _, s := range spans {
			if s.Name() == "registration.enroll" {
				root = s
			}
		}
		if root == nil {
			t.Fatalf("%s: kök span yok", label)
		}
		for _, s := range spans {
			if s.SpanContext().TraceID() != root.SpanContext().TraceID() {
				t.Errorf("%s: %s farklı bir izde", label, s.Name())
			}
			if s.Name() != "registration.enroll" {
				children[s.Name()] = true
				if s.Parent().SpanID() != root.SpanContext().SpanID() {
					t.Errorf("%s: %s kök span'in çocuğu değil", label, s.Name())
				}
			}
		}
		for _, want := range []string{"db.lock_student", "rules.load_input", "rules.check", "db.lock_section", "db.insert_enrollment", "audit.write"} {
			if !children[want] {
				t.Errorf("%s: %q aşaması izde yok (var olanlar: %v)", label, want, children)
			}
		}
		got := ""
		for _, a := range root.Attributes() {
			if string(a.Key) == "enrollment.outcome" {
				got = a.Value.AsString()
			}
		}
		if got != outcome {
			t.Errorf("%s: enrollment.outcome %q, beklenen %q", label, got, outcome)
		}
	}
	check("1. kayıt", first, "enrolled")
	check("2. kayıt (dolu şube)", second, "waitlisted")

	// Reddedilen kayıt (aynı öğrenci tekrar): iş sonucu, hata değil → span Error durumunda OLMAMALI.
	before := len(sr.Ended())
	if _, err := enrollOnce(ctx, pool, ids[0], sectionID); err != ErrAlreadyRegistered {
		t.Fatalf("beklenen ErrAlreadyRegistered: %v", err)
	}
	for _, s := range sr.Ended()[before:] {
		if s.Status().Code.String() == "Error" {
			t.Errorf("iş sonucu (zaten kayıtlı) %q span'ini Error yaptı", s.Name())
		}
	}
}
