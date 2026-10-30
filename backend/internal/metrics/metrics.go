// Package metrics Prometheus metriklerini (HTTP istekleri + alan sayaçları) üretir.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "uninet_http_requests_total", Help: "HTTP istekleri (yöntem, yol deseni, durum koduna göre).",
	}, []string{"method", "route", "status"})

	duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "uninet_http_request_duration_seconds", Help: "HTTP istek süresi.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"method", "route"})

	inflight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "uninet_http_requests_in_flight", Help: "Şu an işlenen HTTP istekleri.",
	})

	// Enrollments kayıt denemelerinin sonucunu sayar: enrolled, waitlisted, rule_violation, closed, ...
	Enrollments = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "uninet_enrollment_attempts_total", Help: "Ders kaydı denemeleri (sonuca göre).",
	}, []string{"outcome"})
)

func init() {
	prometheus.MustRegister(requests, duration, inflight, Enrollments)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Middleware her isteği yol DESENİYLE ("/sections/{id}/enrollments") etiketler; böylece etiket
// sayısı (kardinalite) kimliklerle patlamaz.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		inflight.Inc()
		defer inflight.Dec()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		requests.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// Handler Prometheus'un kazıdığı /metrics ucudur.
func Handler() http.Handler { return promhttp.Handler() }
