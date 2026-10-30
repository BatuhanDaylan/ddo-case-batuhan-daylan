package academic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/rules"
)

// prereqLockKey, ön koşul grafiğini değiştiren işlemleri sıraya sokan advisory kilit anahtarıdır.
// İki yönetici aynı anda birbirini tamamlayan ön koşullar (A→B ve B→A) girerse, kilitsiz durumda
// ikisi de "döngü yok" görüp kaydedebilirdi. Kilit bu yarışı kapatır.
const prereqLockKey = 7001

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadPrereqGraph(ctx context.Context, q queryer) (map[int64]rules.Node, error) {
	rows, err := q.Query(ctx, `SELECT course_id, expr FROM course_prerequisites`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g := map[int64]rules.Node{}
	for rows.Next() {
		var id int64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var n rules.Node
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		g[id] = n
	}
	return g, rows.Err()
}

func courseCodes(ctx context.Context, q queryer, ids []int64) (map[int64]string, error) {
	rows, err := q.Query(ctx, `SELECT id, code FROM courses WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]string{}
	for rows.Next() {
		var id int64
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		m[id] = code
	}
	return m, rows.Err()
}

func cycleMessage(ctx context.Context, q queryer, cycle []int64) string {
	codes, _ := courseCodes(ctx, q, cycle)
	parts := make([]string, len(cycle))
	for i, id := range cycle {
		if c, ok := codes[id]; ok {
			parts[i] = c
		} else {
			parts[i] = "#" + strconv.FormatInt(id, 10)
		}
	}
	return "ön koşul döngüsü: " + strings.Join(parts, " → ")
}

func (h *Handler) getPrereq(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var raw []byte
	err := h.pool.QueryRow(r.Context(), `SELECT expr FROM course_prerequisites WHERE course_id = $1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.JSON(w, http.StatusOK, map[string]any{"course_id": id, "expr": nil})
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"course_id": id, "expr": json.RawMessage(raw)})
}

// setPrereq: PUT /courses/{id}/prerequisites  {"expr": {"op":"and","args":[…]}}
func (h *Handler) setPrereq(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Expr rules.Node `json:"expr"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := in.Expr.Validate(); err != nil {
		httpx.Error(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, prereqLockKey); err != nil {
		internal(w, err)
		return
	}

	referenced := append(in.Expr.Courses(), id)
	var found int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM courses WHERE id = ANY($1)`, uniq(referenced)).Scan(&found); err != nil {
		internal(w, err)
		return
	}
	if found != len(uniq(referenced)) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_reference", "ifadedeki derslerden biri bulunamadı")
		return
	}

	graph, err := loadPrereqGraph(ctx, tx)
	if err != nil {
		internal(w, err)
		return
	}
	old, hadOld := graph[id]
	graph[id] = in.Expr
	if cycle := rules.DetectCycle(graph); cycle != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "prerequisite_cycle", cycleMessage(ctx, tx, cycle))
		return
	}

	raw, _ := json.Marshal(in.Expr)
	if _, err := tx.Exec(ctx,
		`INSERT INTO course_prerequisites (course_id, expr) VALUES ($1, $2)
		 ON CONFLICT (course_id) DO UPDATE SET expr = EXCLUDED.expr`, id, raw); err != nil {
		internal(w, err)
		return
	}
	p, _ := auth.PrincipalFrom(ctx)
	var oldVal any
	if hadOld {
		oldVal = old
	}
	if err := audit.Write(ctx, tx, audit.Entry{
		ActorID: &p.UserID, Action: "course.prerequisites_set", Entity: "course",
		EntityID: strconv.FormatInt(id, 10), Old: oldVal, New: in.Expr, IP: r.RemoteAddr,
	}); err != nil {
		internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"course_id": id, "expr": in.Expr})
}

func (h *Handler) deletePrereq(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var raw []byte
	err = tx.QueryRow(ctx, `DELETE FROM course_prerequisites WHERE course_id = $1 RETURNING expr`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "bu ders için ön koşul tanımlı değil")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	p, _ := auth.PrincipalFrom(ctx)
	if err := audit.Write(ctx, tx, audit.Entry{
		ActorID: &p.UserID, Action: "course.prerequisites_deleted", Entity: "course",
		EntityID: strconv.FormatInt(id, 10), Old: json.RawMessage(raw), IP: r.RemoteAddr,
	}); err != nil {
		internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func uniq(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
