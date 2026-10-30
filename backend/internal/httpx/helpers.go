package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Internal beklenmeyen hatayı loglar ve istemciye ayrıntı sızdırmadan yanıt verir.
//
// Veritabanı geçici olarak ulaşılamıyorsa (yeniden başlatma, ağ kopması, zaman aşımı) 500 DEĞİL,
// "yeniden denenebilir" anlamına gelen 503 + Retry-After döner: istemciler ve yük dengeleyiciler 500'ü kod hatası sayıp
// yeniden denemez, 503'ü ise dener. (Bu ayrım, veritabanı yeniden başlatma kaos testinde 804 isteğin 500 dönmesiyle fark edildi.)
func Internal(w http.ResponseWriter, err error) {
	if IsTransient(err) {
		slog.Warn("geçici hata (yeniden denenebilir)", "err", err)
		w.Header().Set("Retry-After", "1")
		Error(w, http.StatusServiceUnavailable, "service_unavailable", "hizmet geçici olarak kullanılamıyor, lütfen tekrar deneyin")
		return
	}
	slog.Error("istek hatası", "err", err)
	Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
}

// IsTransient hatanın geçici (altyapı kaynaklı, yeniden denenebilir) olup olmadığını söyler:
// bağlantı hataları, bağlantının kopması, zaman aşımı ve PostgreSQL'in "kapanıyor/bağlantı kabul etmiyor" sınıfı.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// 08 = bağlantı istisnası; 57P01 admin_shutdown, 57P02 crash_shutdown, 57P03 cannot_connect_now.
		return strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57P")
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EPIPE),
		errors.As(err, &netErr),
		pgconn.Timeout(err),
		pgconn.SafeToRetry(err):
		return true
	}
	// pgx bazı bağlantı hatalarını yalnızca metin olarak taşır ("failed to connect", "conn closed", "closed pool").
	msg := err.Error()
	return strings.Contains(msg, "failed to connect") || strings.Contains(msg, "conn closed") ||
		strings.Contains(msg, "closed pool") || strings.Contains(msg, "unexpected EOF")
}

// DBError bilinen kısıt ihlallerini anlamlı HTTP kodlarına çevirir; çevrilemezse false döner.
func DBError(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		Error(w, http.StatusConflict, "conflict", "kayıt zaten mevcut")
	case "23503": // foreign_key_violation
		Error(w, http.StatusUnprocessableEntity, "invalid_reference", "başvurulan kayıt bulunamadı")
	case "23514": // check_violation
		Error(w, http.StatusBadRequest, "validation_error", "değer izin verilen aralıkta değil")
	default:
		return false
	}
	return true
}

// PathID {id} yol parametresini pozitif tam sayı olarak okur.
func PathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		Error(w, http.StatusBadRequest, "invalid_id", "geçersiz kimlik")
		return 0, false
	}
	return id, true
}

// Decode gövdeyi 1 MiB ile sınırlar ve bilinmeyen alanları reddeder.
func Decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		Error(w, http.StatusBadRequest, "invalid_body", "istek gövdesi geçersiz")
		return false
	}
	return true
}
