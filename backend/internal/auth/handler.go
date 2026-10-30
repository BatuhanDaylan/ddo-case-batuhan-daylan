package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

type Handler struct {
	pool       *pgxpool.Pool
	iss        *Issuer
	refreshTTL time.Duration
	dummyHash  string // kullanıcı bulunamadığında da argon2 çalıştırmak için (zamanlama sızıntısını önler)
}

func NewHandler(pool *pgxpool.Pool, iss *Issuer, refreshTTL time.Duration) (*Handler, error) {
	dummy, err := HashPassword("dummy-password-for-timing")
	if err != nil {
		return nil, err
	}
	return &Handler{pool: pool, iss: iss, refreshTTL: refreshTTL, dummyHash: dummy}, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type userRow struct {
	ID       int64
	Email    string
	Hash     string
	FullName string
	Role     string
}

// decode gövdeyi sınırlar, bilinmeyen alanları reddeder.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_body", "istek gövdesi geçersiz")
		return false
	}
	return true
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || in.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "email ve password zorunlu")
		return
	}

	var u userRow
	err := h.pool.QueryRow(r.Context(),
		`SELECT id, email, password_hash, full_name, role FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email, &u.Hash, &u.FullName, &u.Role)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("login sorgusu", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
		return
	}
	hash := u.Hash
	if !found {
		hash = h.dummyHash
	}
	ok := VerifyPassword(in.Password, hash) && found
	if !ok {
		h.auditLogin(r, nil, "auth.login_failed", email)
		httpx.Error(w, http.StatusUnauthorized, "invalid_credentials", "e-posta veya parola hatalı")
		return
	}

	resp, err := h.issue(r, h.pool, u.ID, u.Role)
	if err != nil {
		slog.Error("token üretimi", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
		return
	}
	h.auditLogin(r, &u.ID, "auth.login", email)
	httpx.JSON(w, http.StatusOK, resp)
}

// Refresh tek kullanımlık (rotasyonlu) refresh token ile yeni çift üretir.
// Daha önce iptal edilmiş bir token tekrar gelirse çalınmış olabilir: kullanıcının tüm oturumları kapatılır.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.RefreshToken == "" {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "refresh_token zorunlu")
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var (
		tokenID, userID int64
		expires         time.Time
		revoked         *time.Time
		role            string
	)
	err = tx.QueryRow(r.Context(),
		`SELECT t.id, t.user_id, t.expires_at, t.revoked_at, u.role
		   FROM refresh_tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.token_hash = $1 FOR UPDATE OF t`, HashRefreshToken(in.RefreshToken)).
		Scan(&tokenID, &userID, &expires, &revoked, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusUnauthorized, "invalid_token", "refresh token geçersiz")
		return
	}
	if err != nil {
		slog.Error("refresh sorgusu", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
		return
	}

	if revoked != nil {
		// Yeniden kullanım tespiti: bu kullanıcının tüm aktif token'larını iptal et ve kalıcı yap.
		_, _ = tx.Exec(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		_ = audit.Write(r.Context(), tx, audit.Entry{ActorID: &userID, Action: "auth.refresh_reuse",
			Entity: "user", EntityID: itoa(userID), IP: clientIP(r)})
		_ = tx.Commit(r.Context())
		httpx.Error(w, http.StatusUnauthorized, "invalid_token", "refresh token geçersiz")
		return
	}
	if time.Now().After(expires) {
		httpx.Error(w, http.StatusUnauthorized, "invalid_token", "refresh token süresi dolmuş")
		return
	}

	if _, err := tx.Exec(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`, tokenID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
		return
	}
	resp, err := h.issue(r, tx, userID, role)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal", "beklenmeyen hata")
		return
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	// Token bilinmese de 204 dönüyoruz: var olup olmadığı dışarıya sızmasın.
	_, _ = h.pool.Exec(r.Context(),
		`UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		HashRefreshToken(in.RefreshToken))
	w.WriteHeader(http.StatusNoContent)
}

// Me oturum açmış kullanıcının bilgisini döner.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var u userRow
	err := h.pool.QueryRow(r.Context(),
		`SELECT id, email, '', full_name, role FROM users WHERE id = $1`, p.UserID).
		Scan(&u.ID, &u.Email, &u.Hash, &u.FullName, &u.Role)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "kullanıcı bulunamadı")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "email": u.Email, "full_name": u.FullName, "role": u.Role,
	})
}

// issue yeni access + refresh çifti üretir ve refresh özetini kaydeder.
func (h *Handler) issue(r *http.Request, ex audit.Execer, userID int64, role string) (tokenResponse, error) {
	access, err := h.iss.Access(userID, role)
	if err != nil {
		return tokenResponse{}, err
	}
	refresh, hash, err := NewRefreshToken()
	if err != nil {
		return tokenResponse{}, err
	}
	_, err = ex.Exec(r.Context(),
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hash, time.Now().Add(h.refreshTTL))
	if err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{
		AccessToken: access, RefreshToken: refresh, TokenType: "Bearer",
		ExpiresIn: int(h.iss.AccessTTL().Seconds()),
	}, nil
}

func (h *Handler) auditLogin(r *http.Request, actor *int64, action, email string) {
	// Parola asla loglanmaz/denetime yazılmaz; yalnızca e-posta.
	err := audit.Write(r.Context(), h.pool, audit.Entry{
		ActorID: actor, Action: action, Entity: "user", New: map[string]string{"email": email}, IP: clientIP(r),
	})
	if err != nil {
		slog.Error("denetim kaydı yazılamadı", "err", err)
	}
}
