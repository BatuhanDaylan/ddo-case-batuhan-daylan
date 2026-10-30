package documents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/access"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/idempotency"
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

	// Idempotency-Key isteğe bağlıdır: verilirse aynı anahtarla gelen tekrar istek yeni belge üretmeden
	// ilk yanıtı aynen döner (belge ve anahtar aynı transaction'da yazılır).
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if len(key) > 255 {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "Idempotency-Key en fazla 255 karakter olabilir")
			return
		}
		res, err := idempotency.Do(r.Context(), h.pool, p.UserID, key, "POST /students/"+strconv.FormatInt(st.ID, 10)+"/transcripts",
			func(tx pgx.Tx) (int, []byte, error) {
				out, err := h.svc.IssueTx(r.Context(), tx, st.ID, p.UserID, r.RemoteAddr)
				if err != nil {
					return 0, nil, err
				}
				body, err := json.Marshal(out)
				return http.StatusCreated, body, err
			})
		if errors.Is(err, idempotency.ErrKeyReuse) {
			httpx.Error(w, http.StatusUnprocessableEntity, "idempotency_key_reuse", "bu Idempotency-Key farklı bir istek için zaten kullanıldı")
			return
		}
		if err != nil {
			httpx.Internal(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if res.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		w.WriteHeader(res.Status)
		_, _ = w.Write(append(res.Body, '\n'))
		return
	}

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
