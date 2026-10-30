// Package access öğrenci verilerine erişim yetkisini tek yerde toplar (T-7: kim kimi görebilir).
package access

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

type Student struct {
	ID, UserID, ProgramID int64
	EntryYear             int
	AdvisorID             *int64
}

// Authorize öğrenciyi yükler ve çağıranın görme yetkisini denetler: öğrenci yalnızca kendisini,
// danışman yalnızca danışanlarını; öğrenci işleri ve bölüm sekreteri tümünü görür; diğer roller 403 alır.
// false dönerse yanıt zaten yazılmıştır.
func Authorize(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, id int64) (Student, bool) {
	st := Student{ID: id}
	err := pool.QueryRow(r.Context(),
		`SELECT user_id, program_id, entry_year, advisor_id FROM students WHERE id = $1`, id).
		Scan(&st.UserID, &st.ProgramID, &st.EntryYear, &st.AdvisorID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "öğrenci bulunamadı")
		return st, false
	}
	if err != nil {
		httpx.Internal(w, err)
		return st, false
	}

	p, _ := auth.PrincipalFrom(r.Context())
	allowed := false
	switch p.Role {
	case auth.RoleRegistrar, auth.RoleDeptSecretary:
		allowed = true
	case auth.RoleStudent:
		allowed = p.UserID == st.UserID
	case auth.RoleAdvisor:
		allowed = st.AdvisorID != nil && *st.AdvisorID == p.UserID
	}
	if !allowed {
		httpx.Error(w, http.StatusForbidden, "forbidden", "bu öğrenci için yetkiniz yok")
		return st, false
	}
	return st, true
}
