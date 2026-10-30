// Package academic akademik yapıyı (fakülte → bölüm → program → müfredat sürümleri) yönetir.
package academic

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func NewHandler(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// Routes çağıranın Authenticate middleware'i arkasında bağlanmalıdır.
func (h *Handler) Routes(r chi.Router) {
	writers := auth.Require(auth.RoleRegistrar, auth.RoleDeptSecretary)

	r.Get("/faculties", h.listFaculties)
	r.With(writers).Post("/faculties", h.createFaculty)

	r.Get("/departments", h.listDepartments)
	r.With(writers).Post("/departments", h.createDepartment)

	r.Get("/programs", h.listPrograms)
	r.With(writers).Post("/programs", h.createProgram)

	r.Get("/courses", h.listCourses)
	r.With(writers).Post("/courses", h.createCourse)

	r.Get("/courses/{id}/prerequisites", h.getPrereq)
	r.With(writers).Put("/courses/{id}/prerequisites", h.setPrereq)
	r.With(writers).Delete("/courses/{id}/prerequisites", h.deletePrereq)

	r.Get("/programs/{id}/curriculum-versions", h.listVersions)
	r.With(writers).Post("/programs/{id}/curriculum-versions", h.createVersion)
	r.Get("/curriculum-versions/{id}", h.getVersion)

	r.Get("/students/{id}/curriculum", h.studentCurriculum)
	r.Get("/students/{id}/required-courses", h.requiredCourses)
	// Muafiyeti yalnızca öğrenci işleri verir; öğrencinin kendisi (authorizeStudent'tan geçse de) veremez.
	r.With(auth.Require(auth.RoleRegistrar)).Post("/students/{id}/exemptions", h.createExemption)

	r.Get("/course-equivalences", h.listEquivalences)
	r.With(writers).Post("/course-equivalences", h.createEquivalence)
	r.With(writers).Delete("/course-equivalences/{id}", h.deleteEquivalence)
}

// --- ortak yardımcılar ---

func internal(w http.ResponseWriter, err error) {
	slog.Error("istek hatası", "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
}

// dbError bilinen kısıt ihlallerini anlamlı HTTP kodlarına çevirir; çevrilemezse false döner.
func dbError(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		httpx.Error(w, http.StatusConflict, "conflict", "kayıt zaten mevcut")
	case "23503": // foreign_key_violation
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_reference", "başvurulan kayıt bulunamadı")
	case "23514": // check_violation
		httpx.Error(w, http.StatusBadRequest, "validation_error", "değer izin verilen aralıkta değil")
	default:
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		httpx.Error(w, http.StatusBadRequest, "invalid_id", "geçersiz kimlik")
		return 0, false
	}
	return id, true
}

// optInt64 ?name=<n> süzgecini okur; verilmemişse nil (SQL'de "süzgeç yok" anlamına gelir).
func optInt64(w http.ResponseWriter, r *http.Request, name string) (*int64, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", name+" geçersiz")
		return nil, false
	}
	return &n, true
}

// listPage sorguyu çalıştırıp keyset sayfası döner. Sorgu şu sırayla parametre almalı:
// $1 = after, $2 = limit+1, $3 = isteğe bağlı süzgeç.
func listPage[T any](w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, query string,
	filter *int64, scan func(pgx.Rows) (T, error), id func(T) int64) {
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	rows, err := pool.Query(r.Context(), query, after, limit+1, filter)
	if err != nil {
		internal(w, err)
		return
	}
	defer rows.Close()
	var items []T
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			internal(w, err)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, id))
}

// createAudited kaydı ve denetim satırını tek transaction'da yazar:
// ikisinden biri başarısız olursa hiçbiri kalıcı olmaz.
func (h *Handler) createAudited(ctx context.Context, r *http.Request, entity string,
	insert func(tx pgx.Tx) (int64, error), newValue any) (int64, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := insert(tx)
	if err != nil {
		return 0, err
	}
	p, _ := auth.PrincipalFrom(ctx)
	if err := audit.Write(ctx, tx, audit.Entry{
		ActorID: &p.UserID, Action: entity + ".create", Entity: entity,
		EntityID: strconv.FormatInt(id, 10), New: newValue, IP: r.RemoteAddr,
	}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
