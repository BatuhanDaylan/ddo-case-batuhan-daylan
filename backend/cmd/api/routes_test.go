package main

import (
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/batuhan-daylan/uninet/internal/academic"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/documents"
	"github.com/batuhan-daylan/uninet/internal/gradebook"
	"github.com/batuhan-daylan/uninet/internal/graduation"
	"github.com/batuhan-daylan/uninet/internal/openapi"
	"github.com/batuhan-daylan/uninet/internal/registration"
	"github.com/batuhan-daylan/uninet/internal/reports"
	"github.com/batuhan-daylan/uninet/internal/timetable"
)

// realRoutes gerçek yönlendiricideki tüm "YÖNTEM /yol" çiftlerini döner (veritabanı gerekmez: yalnızca kayıtlar gezilir).
func realRoutes(t *testing.T) []string {
	t.Helper()
	iss := auth.NewIssuer("route-test-secret-route-test-secret-123", time.Minute)
	authH, err := auth.NewHandler(nil, iss, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := documents.NewSigner("route-test-signing-key-route-test-key-1")
	if err != nil {
		t.Fatal(err)
	}
	docs := documents.NewHandler(nil, documents.NewService(nil, signer, "http://x"))
	router := newRouter(nil, iss, authH, academic.NewHandler(nil), registration.NewHandler(nil), gradebook.NewHandler(nil),
		docs, graduation.NewHandler(nil), timetable.NewHandler(nil), reports.NewHandler(nil))

	var out []string
	err = chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestPrintRoutes yardımcıdır: -run TestPrintRoutes -v ile tüm rotaları listeler.
func TestPrintRoutes(t *testing.T) {
	for _, r := range realRoutes(t) {
		fmt.Println(r)
	}
}

// Yol düzeyinde "parameters" gibi liste alanları da bulunabildiği için işlemler genel harita olarak okunur.
type specDoc struct {
	Paths map[string]map[string]any `yaml:"paths"`
}

var httpMethods = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE"}

// OpenAPI belgesi ile gerçek yönlendirici BİREBİR eşleşmelidir: belgelenmemiş rota ya da var olmayan
// bir rotayı belgeleyen kayıt CI'ı kırar.
func TestOpenAPIMatchesRouter(t *testing.T) {
	var doc specDoc
	if err := yaml.Unmarshal(openapi.Spec, &doc); err != nil {
		t.Fatalf("openapi.yaml ayrıştırılamadı: %v", err)
	}
	documented := map[string]bool{}
	for path, ops := range doc.Paths {
		for m, raw := range ops {
			method, ok := httpMethods[m]
			if !ok {
				continue // "parameters" gibi yol düzeyi anahtarlar
			}
			op, _ := raw.(map[string]any)
			key := method + " " + path
			documented[key] = true
			summary, _ := op["summary"].(string)
			responses, _ := op["responses"].(map[string]any)
			roles, _ := op["x-roles"].([]any)
			if summary == "" || len(responses) == 0 || len(roles) == 0 {
				t.Errorf("%s: summary, responses ve x-roles zorunlu", key)
			}
			public := len(roles) == 1 && roles[0] == "public"
			sec, hasSec := op["security"].([]any)
			if public != (hasSec && len(sec) == 0) {
				t.Errorf("%s: x-roles [public] ile 'security: []' birlikte olmalı (ve yalnızca herkese açık uçlarda)", key)
			}
		}
	}

	real := map[string]bool{}
	for _, r := range realRoutes(t) {
		real[r] = true
	}
	for r := range real {
		if !documented[r] {
			t.Errorf("belgelenmemiş rota: %s", r)
		}
	}
	for d := range documented {
		if !real[d] {
			t.Errorf("belgede olup yönlendiricide olmayan rota: %s", d)
		}
	}
	if len(real) < 70 {
		t.Errorf("rota sayısı beklenenden az: %d", len(real))
	}
}
