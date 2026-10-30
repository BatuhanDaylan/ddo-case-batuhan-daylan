package registration

import (
	"net/http"
	"sort"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

// Risk eşikleri (bonus: danışman için riskli öğrenci uyarısı). Devamsızlık verisi sistemde tutulmadığı için
// yalnızca not verisinden türetilen üç sinyal kullanılır.
const (
	riskMinGANO     = 2.00 // GANO bunun altındaysa mezuniyet şartı tehlikede
	riskDropDelta   = 0.50 // son dönem ortalaması bir öncekinden bu kadar düştüyse
	riskFailedCount = 2    // son dönemde bu kadar derse kaldıysa
)

// riskInput bir öğrencinin not özetidir; tamamı veritabanından toplanır, değerlendirme saftır.
type riskInput struct {
	GANO         float64
	LastTermGPA  float64
	PrevTermGPA  float64
	HasPrevTerm  bool
	FailedInLast int
}

// riskReasons uyarı kodlarını döner; boş dilim = risk yok.
func riskReasons(in riskInput) []string {
	var out []string
	if in.GANO < riskMinGANO {
		out = append(out, "low_gano")
	}
	if in.HasPrevTerm && in.PrevTermGPA-in.LastTermGPA >= riskDropDelta {
		out = append(out, "gpa_drop")
	}
	if in.FailedInLast >= riskFailedCount {
		out = append(out, "failed_courses")
	}
	return out
}

type riskRow struct {
	StudentID    int64    `json:"student_id"`
	StudentNo    string   `json:"student_no"`
	GANO         float64  `json:"gano"`
	LastTermGPA  float64  `json:"last_term_gpa"`
	PrevTermGPA  *float64 `json:"prev_term_gpa"`
	FailedInLast int      `json:"failed_in_last_term"`
	Reasons      []string `json:"reasons"`
}

// atRisk: GET /advisor/at-risk
// Danışmanın yalnızca kendi danışanları arasından riskli olanları, en çok uyarısı olandan başlayarak listeler.
// Dönem ortalamaları AKTS ağırlıklıdır; muaf dersler hesaba katılmaz. "Son dönem" öğrencinin sonucu olan en yeni dönemdir.
func (h *Handler) atRisk(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	rows, err := h.pool.Query(r.Context(), `
		WITH per_term AS (
		  SELECT cr.student_id, cr.term_id,
		         sum(cr.grade_point * c.ects) / NULLIF(sum(c.ects), 0) AS gpa,
		         count(*) FILTER (WHERE NOT cr.passed) AS failed,
		         row_number() OVER (PARTITION BY cr.student_id ORDER BY cr.term_id DESC) AS rn
		    FROM course_results cr JOIN courses c ON c.id = cr.course_id
		   WHERE NOT cr.exempt
		   GROUP BY cr.student_id, cr.term_id)
		SELECT st.id, st.student_no,
		       COALESCE((SELECT sum(cr.grade_point * c.ects) / NULLIF(sum(c.ects), 0)
		                   FROM course_results cr JOIN courses c ON c.id = cr.course_id
		                  WHERE cr.student_id = st.id AND NOT cr.exempt), 0)::float8,
		       COALESCE(l.gpa, 0)::float8, pv.gpa::float8, COALESCE(l.failed, 0)::int
		  FROM students st
		  LEFT JOIN per_term l  ON l.student_id  = st.id AND l.rn = 1
		  LEFT JOIN per_term pv ON pv.student_id = st.id AND pv.rn = 2
		 WHERE st.advisor_id = $1 AND l.student_id IS NOT NULL
		 ORDER BY st.id`, p.UserID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	items := []riskRow{}
	for rows.Next() {
		var x riskRow
		if err := rows.Scan(&x.StudentID, &x.StudentNo, &x.GANO, &x.LastTermGPA, &x.PrevTermGPA, &x.FailedInLast); err != nil {
			httpx.Internal(w, err)
			return
		}
		in := riskInput{GANO: x.GANO, LastTermGPA: x.LastTermGPA, FailedInLast: x.FailedInLast}
		if x.PrevTermGPA != nil {
			in.HasPrevTerm, in.PrevTermGPA = true, *x.PrevTermGPA
		}
		if x.Reasons = riskReasons(in); len(x.Reasons) > 0 {
			items = append(items, x)
		}
	}
	if err := rows.Err(); err != nil {
		httpx.Internal(w, err)
		return
	}
	sort.SliceStable(items, func(i, j int) bool { return len(items[i].Reasons) > len(items[j].Reasons) })
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
