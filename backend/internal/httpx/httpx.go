// Package httpx ortak HTTP yardımcılarını içerir (tutarlı hata biçimi vb.).
package httpx

import (
	"encoding/json"
	"net/http"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JSON verilen değeri JSON olarak yazar.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error proje genelinde tek tip hata yanıtı yazar: {"error":{"code","message"}}.
func Error(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}
