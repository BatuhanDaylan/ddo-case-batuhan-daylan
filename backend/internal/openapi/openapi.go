// Package openapi API'nin OpenAPI 3 belgesini gömer ve /openapi.yaml altında sunar.
// Belgenin gerçek yönlendiriciyle birebir eşleştiği cmd/api/routes_test.go içinde sınanır.
package openapi

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var Spec []byte

// Handler belgeyi YAML olarak sunar.
func Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(Spec)
}
