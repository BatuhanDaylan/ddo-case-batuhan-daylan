// Package reports rektörlük/raporlama rolü için toplu raporlar üretir (F-8): bölüm bazlı başarı, doluluk,
// GANO dağılımı. Sorgular salt okunurdur ve toplama veritabanında yapılır.
package reports

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
)

type Handler struct{ pool *pgxpool.Pool }

func NewHandler(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// Routes çağıranın Authenticate middleware'i arkasında bağlanmalıdır. Raporlar yalnızca raporlama,
// öğrenci işleri ve bölüm sekreteri rollerine açıktır.
func (h *Handler) Routes(r chi.Router) {
	allowed := auth.Require(auth.RoleReporting, auth.RoleRegistrar, auth.RoleDeptSecretary)
	r.With(allowed).Get("/reports/department-success", h.departmentSuccess)
	r.With(allowed).Get("/reports/occupancy", h.occupancy)
	r.With(allowed).Get("/reports/gano-distribution", h.ganoDistribution)
}

// termParam ?term_id= zorunlu dönem parametresini okur.
func termParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("term_id"), 10, 64)
	if err != nil || id < 1 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "term_id zorunlu ve pozitif olmalı")
		return 0, false
	}
	return id, true
}

type successRow struct {
	DepartmentID  int64   `json:"department_id"`
	Department    string  `json:"department"`
	Results       int     `json:"results"`
	Passed        int     `json:"passed"`
	PassRate      float64 `json:"pass_rate"`
	AvgGradePoint float64 `json:"avg_grade_point"`
}

// departmentSuccess: GET /reports/department-success?term_id=
// Dönemde bölüm bazında: sonuç sayısı, geçen sayısı, geçme oranı ve ortalama katsayı (muaf dersler hariç).
func (h *Handler) departmentSuccess(w http.ResponseWriter, r *http.Request) {
	termID, ok := termParam(w, r)
	if !ok {
		return
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT d.id, d.name, count(*)::int,
		        count(*) FILTER (WHERE cr.passed)::int,
		        COALESCE(round(avg(cr.grade_point)::numeric, 2), 0)::float8
		   FROM course_results cr
		   JOIN courses c ON c.id = cr.course_id
		   JOIN departments d ON d.id = c.department_id
		  WHERE cr.term_id = $1 AND NOT cr.exempt
		  GROUP BY d.id, d.name ORDER BY d.id`, termID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	items := []successRow{}
	for rows.Next() {
		var x successRow
		if err := rows.Scan(&x.DepartmentID, &x.Department, &x.Results, &x.Passed, &x.AvgGradePoint); err != nil {
			httpx.Internal(w, err)
			return
		}
		if x.Results > 0 {
			x.PassRate = float64(x.Passed*10000/x.Results) / 100
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"term_id": termID, "items": items})
}

type occupancyRow struct {
	DepartmentID int64   `json:"department_id"`
	Department   string  `json:"department"`
	Sections     int     `json:"sections"`
	Capacity     int     `json:"capacity"`
	Enrolled     int     `json:"enrolled"`
	Waitlisted   int     `json:"waitlisted"`
	FillRate     float64 `json:"fill_rate"`
}

// occupancy: GET /reports/occupancy?term_id=
// Dönemde bölüm bazında şube doluluğu: kontenjan, kayıtlı, bekleyen ve doluluk oranı.
func (h *Handler) occupancy(w http.ResponseWriter, r *http.Request) {
	termID, ok := termParam(w, r)
	if !ok {
		return
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT d.id, d.name, count(*)::int, sum(s.capacity)::int, sum(s.enrolled_count)::int,
		        COALESCE(sum(w.n), 0)::int
		   FROM sections s
		   JOIN courses c ON c.id = s.course_id
		   JOIN departments d ON d.id = c.department_id
		   LEFT JOIN LATERAL (SELECT count(*) AS n FROM enrollments e WHERE e.section_id = s.id AND e.status = 'waitlisted') w ON true
		  WHERE s.term_id = $1
		  GROUP BY d.id, d.name ORDER BY d.id`, termID)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	items := []occupancyRow{}
	for rows.Next() {
		var x occupancyRow
		if err := rows.Scan(&x.DepartmentID, &x.Department, &x.Sections, &x.Capacity, &x.Enrolled, &x.Waitlisted); err != nil {
			httpx.Internal(w, err)
			return
		}
		if x.Capacity > 0 {
			x.FillRate = float64(x.Enrolled*10000/x.Capacity) / 100
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"term_id": termID, "items": items})
}

type bucket struct {
	Range string `json:"range"`
	Count int    `json:"count"`
}

// ganoDistribution: GET /reports/gano-distribution[?department_id=&program_id=]
// Öğrencilerin (önbellekteki) GANO'sunun aralıklara dağılımı. Boş süzgeç: tüm öğrenciler.
func (h *Handler) ganoDistribution(w http.ResponseWriter, r *http.Request) {
	var dept, prog *int64
	for name, dst := range map[string]**int64{"department_id": &dept, "program_id": &prog} {
		if v := r.URL.Query().Get(name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 {
				httpx.Error(w, http.StatusBadRequest, "validation_error", name+" geçersiz")
				return
			}
			*dst = &n
		}
	}
	// width_bucket: [0,1) [1,2) [2,2.5) [2.5,3) [3,3.5) [3.5,4]. Üst sınır dahil olsun diye 4.0 son aralığa alınır.
	rows, err := h.pool.Query(r.Context(),
		`SELECT width_bucket(LEAST(s.gano, 3.999)::numeric, ARRAY[0, 1, 2, 2.5, 3, 3.5]::numeric[]) AS b, count(*)::int
		   FROM students s JOIN programs p ON p.id = s.program_id
		  WHERE ($1::bigint IS NULL OR p.department_id = $1) AND ($2::bigint IS NULL OR s.program_id = $2)
		  GROUP BY b ORDER BY b`, dept, prog)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	labels := []string{"0.00-0.99", "1.00-1.99", "2.00-2.49", "2.50-2.99", "3.00-3.49", "3.50-4.00"}
	counts := make([]int, len(labels))
	total := 0
	for rows.Next() {
		var b, n int
		if err := rows.Scan(&b, &n); err != nil {
			httpx.Internal(w, err)
			return
		}
		if b >= 1 && b <= len(labels) {
			counts[b-1] = n
			total += n
		}
	}
	if err := rows.Err(); err != nil {
		httpx.Internal(w, err)
		return
	}
	buckets := make([]bucket, len(labels))
	for i := range labels {
		buckets[i] = bucket{Range: labels[i], Count: counts[i]}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"total_students": total, "buckets": buckets})
}
