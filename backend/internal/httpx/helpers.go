package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Internal beklenmeyen hatayı loglar ve istemciye ayrıntı sızdırmadan 500 döner.
func Internal(w http.ResponseWriter, err error) {
	slog.Error("istek hatası", "err", err)
	Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
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
