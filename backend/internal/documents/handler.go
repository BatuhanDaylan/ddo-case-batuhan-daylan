package documents

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/access"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

type Handler struct {
	pool *pgxpool.Pool
	svc  *Service
}

func NewHandler(pool *pgxpool.Pool, svc *Service) *Handler { return &Handler{pool: pool, svc: svc} }

// Routes kimlik doğrulaması gerektiren uç noktalar (Authenticate middleware'i arkasında bağlanır).
func (h *Handler) Routes(r chi.Router) {
	r.Post("/students/{id}/transcripts", h.issueTranscript)
	r.Get("/documents/{id}/pdf", h.pdf)
}

// PublicRoutes kimlik gerektirmeyen doğrulama ucu. Kodun kendisi (HMAC imzalı) yetkidir.
func (h *Handler) PublicRoutes(r chi.Router) {
	r.Get("/verify/{code}", h.verify)
}

// issueTranscript: POST /students/{id}/transcripts
// Öğrenci kendi, danışman danışanlarının, öğrenci işleri/bölüm sekreteri herkesin belgesini düzenleyebilir.
func (h *Handler) issueTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	st, ok := access.Authorize(w, r, h.pool, id)
	if !ok {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	out, err := h.svc.Issue(r.Context(), st.ID, p.UserID, r.RemoteAddr)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// pdf: GET /documents/{id}/pdf — belgenin sahibi öğrenci için yetkisi olan herkes indirebilir.
func (h *Handler) pdf(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	studentID, err := h.svc.StudentOf(r.Context(), id)
	if errors.Is(err, ErrDocumentNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "belge bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if _, ok := access.Authorize(w, r, h.pool, studentID); !ok {
		return
	}
	b, err := h.svc.PDF(r.Context(), id)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="transkript-`+strconv.FormatInt(id, 10)+`.pdf"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// verify: GET /verify/{code} — herkese açık. Her başarısızlık aynı 404 yanıtını verir:
// "böyle bir belge yok" ile "imza tutmuyor" arasındaki fark dışarıya sızmaz.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.Verify(r.Context(), chi.URLParam(r, "code"))
	if errors.Is(err, ErrInvalidCode) {
		httpx.Error(w, http.StatusNotFound, "invalid_document", "belge doğrulanamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
