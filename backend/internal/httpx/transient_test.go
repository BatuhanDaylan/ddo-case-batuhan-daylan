package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransient(t *testing.T) {
	transient := []error{
		context.DeadlineExceeded,
		io.EOF, io.ErrUnexpectedEOF,
		syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE,
		&pgconn.PgError{Code: "57P01"}, // admin_shutdown (db yeniden başlatılıyor)
		&pgconn.PgError{Code: "57P03"}, // cannot_connect_now (db açılıyor)
		&pgconn.PgError{Code: "08006"}, // connection_failure
		errors.New("failed to connect to `host=db`: dial error"),
		fmt.Errorf("sarmalanmış: %w", io.ErrUnexpectedEOF),
	}
	for _, e := range transient {
		if !IsTransient(e) {
			t.Errorf("geçici sayılmalıydı: %v", e)
		}
	}
	permanent := []error{
		nil,
		errors.New("beklenmeyen bir mantık hatası"),
		&pgconn.PgError{Code: "23505"}, // unique_violation: kod/veri hatası, yeniden denemek çözmez
		&pgconn.PgError{Code: "42P01"}, // undefined_table
	}
	for _, e := range permanent {
		if IsTransient(e) {
			t.Errorf("kalıcı sayılmalıydı: %v", e)
		}
	}
}

// Veritabanı kesintisinde 500 değil, yeniden denenebilir 503 + Retry-After dönmeli; gerçek hata 500 kalmalı.
func TestInternalMapsOutageTo503(t *testing.T) {
	rec := httptest.NewRecorder()
	Internal(rec, &pgconn.PgError{Code: "57P01"})
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("kesinti: %d Retry-After=%q, beklenen 503 + Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	rec = httptest.NewRecorder()
	Internal(rec, errors.New("gerçek hata"))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Retry-After") != "" {
		t.Errorf("gerçek hata: %d Retry-After=%q, beklenen 500", rec.Code, rec.Header().Get("Retry-After"))
	}
}
