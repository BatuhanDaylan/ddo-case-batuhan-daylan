package graduation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/academic"
	"github.com/batuhan-daylan/uninet/internal/access"
	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/gradebook"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/rules"
)

type Handler struct{ pool *pgxpool.Pool }

func NewHandler(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// Routes çağıranın Authenticate middleware'i arkasında bağlanmalıdır.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/curriculum-versions/{id}/graduation-rules", h.getRules)
	r.With(auth.Require(auth.RoleRegistrar, auth.RoleDeptSecretary)).
		Put("/curriculum-versions/{id}/graduation-rules", h.putRules)
	r.Get("/students/{id}/graduation-audit", h.audit)
}

func (h *Handler) getRules(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var raw []byte
	err := h.pool.QueryRow(r.Context(), `SELECT rules FROM graduation_rules WHERE curriculum_version_id = $1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "bu müfredat için mezuniyet kuralı tanımlı değil")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"curriculum_version_id": id, "rules": json.RawMessage(raw)})
}

// putRules kuralları doğrular (yapı + derslerin varlığı) ve kaydeder. Eski/yeni değer denetime yazılır.
func (h *Handler) putRules(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in Rules
	if !httpx.Decode(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		httpx.Error(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	ctx := r.Context()
	codes := in.AllCodes()
	var found int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM courses WHERE code = ANY($1)`, codes).Scan(&found); err != nil {
		httpx.Internal(w, err)
		return
	}
	if found != len(codes) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_reference", "kurallardaki derslerden biri bulunamadı")
		return
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldRaw []byte
	_ = tx.QueryRow(ctx, `SELECT rules FROM graduation_rules WHERE curriculum_version_id = $1 FOR UPDATE`, id).Scan(&oldRaw)

	raw, _ := json.Marshal(in)
	p, _ := auth.PrincipalFrom(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO graduation_rules (curriculum_version_id, rules, updated_by) VALUES ($1, $2, $3)
		 ON CONFLICT (curriculum_version_id) DO UPDATE SET rules = EXCLUDED.rules, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		id, raw, p.UserID); err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	var oldVal any
	if oldRaw != nil {
		oldVal = json.RawMessage(oldRaw)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "graduation_rules.set", Entity: "curriculum_version",
		EntityID: strconv.FormatInt(id, 10), Old: oldVal, New: in, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"curriculum_version_id": id, "rules": in})
}

type remainingCourse struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	ECTS     int    `json:"ects"`
	Semester int    `json:"semester,omitempty"`
}

// audit: GET /students/{id}/graduation-audit
// Öğrencinin giriş yılındaki müfredatın kurallarına göre "mezun olabilir" ya da "şu dersler eksik" der.
func (h *Handler) audit(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	st, ok := access.Authorize(w, r, h.pool, id)
	if !ok {
		return
	}
	ctx := r.Context()

	vid, err := academic.ResolveVersion(ctx, h.pool, st.ProgramID, st.EntryYear)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "no_curriculum", "bu giriş yılı için tanımlı müfredat yok")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	var raw []byte
	err = h.pool.QueryRow(ctx, `SELECT rules FROM graduation_rules WHERE curriculum_version_id = $1`, vid).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "no_rules", "bu müfredat için mezuniyet kuralı tanımlı değil")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	var rl Rules
	if err := json.Unmarshal(raw, &rl); err != nil {
		httpx.Internal(w, err)
		return
	}

	in, remaining, err := h.buildInput(ctx, st.ID, vid, rl)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	rep, err := Evaluate(in)
	if errors.Is(err, ErrTooComplex) {
		httpx.Error(w, http.StatusUnprocessableEntity, "rules_too_complex", err.Error())
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	// Eksik zorunlu derslerin ad/AKTS/yarıyıl bilgisi ("kalan dersler" raporu).
	rem := []remainingCourse{}
	for _, code := range rep.RequiredMissing {
		if c, ok := remaining[code]; ok {
			rem = append(rem, c)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"student_id": st.ID, "entry_year": st.EntryYear, "curriculum_version_id": vid,
		"report": rep, "remaining_required_courses": rem,
	})
}

// buildInput öğrencinin başarı durumunu kural motorunun girdisine çevirir. Denklik ve muafiyet,
// not motorundaki/kayıt doğrulayıcıdaki aynı rules.ExpandPassed ile uygulanır.
func (h *Handler) buildInput(ctx context.Context, studentID, versionID int64, rl Rules) (Input, map[string]remainingCourse, error) {
	in := Input{Rules: rl, Passed: map[string]int{}}

	rows, err := h.pool.Query(ctx,
		`SELECT DISTINCT cr.course_id FROM course_results cr WHERE cr.student_id = $1 AND cr.passed`, studentID)
	if err != nil {
		return in, nil, err
	}
	actual := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return in, nil, err
		}
		actual[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, nil, err
	}

	eq := rules.Equivalences{}
	erows, err := h.pool.Query(ctx, `SELECT course_id, substitute_course_id FROM course_equivalences`)
	if err != nil {
		return in, nil, err
	}
	for erows.Next() {
		var c, s int64
		if err := erows.Scan(&c, &s); err != nil {
			erows.Close()
			return in, nil, err
		}
		eq[c] = append(eq[c], s)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return in, nil, err
	}
	expanded := rules.ExpandPassed(actual, eq)

	ids := make([]int64, 0, len(expanded))
	for id := range expanded {
		ids = append(ids, id)
	}
	crows, err := h.pool.Query(ctx, `SELECT id, code, ects FROM courses WHERE id = ANY($1)`, ids)
	if err != nil {
		return in, nil, err
	}
	for crows.Next() {
		var id int64
		var code string
		var ects int
		if err := crows.Scan(&id, &code, &ects); err != nil {
			crows.Close()
			return in, nil, err
		}
		in.Passed[code] = ects
		if actual[id] { // toplam AKTS'ye yalnızca gerçekten başarılan dersler girer (denklik çift saymaz)
			in.TotalECTS += ects
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return in, nil, err
	}

	// Müfredat dersleri: zorunluluğun kaynağı ve kalan ders ayrıntıları.
	remaining := map[string]remainingCourse{}
	var currRequired []string
	mrows, err := h.pool.Query(ctx,
		`SELECT c.code, c.name, c.ects, cc.semester, cc.kind
		   FROM curriculum_courses cc JOIN courses c ON c.id = cc.course_id
		  WHERE cc.curriculum_version_id = $1 ORDER BY cc.semester, c.code`, versionID)
	if err != nil {
		return in, nil, err
	}
	for mrows.Next() {
		var rc remainingCourse
		var kind string
		if err := mrows.Scan(&rc.Code, &rc.Name, &rc.ECTS, &rc.Semester, &kind); err != nil {
			mrows.Close()
			return in, nil, err
		}
		remaining[rc.Code] = rc
		if kind == "required" {
			currRequired = append(currRequired, rc.Code)
		}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return in, nil, err
	}
	in.Required = rl.Required
	if len(in.Required) == 0 {
		in.Required = currRequired
	}
	// Kurallarda adı geçip müfredatta olmayan zorunlu dersler için ad/AKTS bilgisi.
	for _, code := range in.Required {
		if _, ok := remaining[code]; !ok {
			var rc remainingCourse
			if err := h.pool.QueryRow(ctx, `SELECT code, name, ects FROM courses WHERE code = $1`, code).Scan(&rc.Code, &rc.Name, &rc.ECTS); err == nil {
				remaining[code] = rc
			}
		}
	}

	g, err := gradebook.LoadGANO(ctx, h.pool, studentID)
	if err != nil {
		return in, nil, err
	}
	in.GANO = g
	slices.Sort(in.Required)
	return in, remaining, nil
}

// SeedDemo her müfredat sürümüne örnek bir mezuniyet kuralı ekler (yoksa). İdempotenttir.
// Seçmeli havuzlar bilerek çakışır: BM301 hem "Yapay Zekâ" hem "Genel Seçmeli" havuzunda geçerlidir.
func SeedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	demo := Rules{
		MinGANO: 2.0, MinTotalECTS: 240,
		Pools: []Pool{
			{Name: "Yapay Zekâ", Courses: []string{"BM301", "BM302"}, MinCourses: 1, MinECTS: 4},
			{Name: "Genel Seçmeli", Courses: []string{"BM301", "BM302", "BM303"}, MinCourses: 2, MinECTS: 7},
		},
	}
	raw, err := json.Marshal(demo)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO graduation_rules (curriculum_version_id, rules)
		 SELECT id, $1 FROM curriculum_versions ON CONFLICT (curriculum_version_id) DO NOTHING`, raw)
	return err
}
