// Package timetable ders programı çözücüsünü veritabanına ve HTTP'ye bağlar.
package timetable

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/scheduler"
)

const (
	firstSlotMinute = 9 * 60 // ilk ders saati 09:00
	slotMinutes     = 60
	maxTimeLimit    = 60 * time.Second
)

type Handler struct{ pool *pgxpool.Pool }

func NewHandler(pool *pgxpool.Pool) *Handler { return &Handler{pool: pool} }

// Routes çağıranın Authenticate middleware'i arkasında bağlanmalıdır.
func (h *Handler) Routes(r chi.Router) {
	admins := auth.Require(auth.RoleRegistrar, auth.RoleDeptSecretary)
	r.Get("/rooms", h.listRooms)
	r.With(admins).Post("/rooms", h.createRoom)
	r.With(admins).Put("/instructors/{id}/unavailability", h.setUnavailability)
	r.With(admins).Post("/terms/{id}/schedule/solve", h.solve)
	r.Get("/terms/{id}/schedule", h.getSchedule)
	r.With(admins).Patch("/section-meetings/{id}", h.moveMeeting)
}

type room struct {
	ID       int64  `json:"id"`
	Code     string `json:"code"`
	Capacity int    `json:"capacity"`
	Kind     string `json:"kind"`
}

func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) {
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT id, code, capacity, kind FROM rooms WHERE id > $1 ORDER BY id LIMIT $2`, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	var items []room
	for rows.Next() {
		var x room
		if err := rows.Scan(&x.ID, &x.Code, &x.Capacity, &x.Kind); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, x)
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x room) int64 { return x.ID }))
}

func (h *Handler) createRoom(w http.ResponseWriter, r *http.Request) {
	var in room
	if !httpx.Decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Code) == "" || in.Capacity < 1 || !slices.Contains([]string{"lecture", "lab", "amphi"}, in.Kind) {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "code, capacity>0 ve kind (lecture|lab|amphi) zorunlu")
		return
	}
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `INSERT INTO rooms (code, capacity, kind) VALUES ($1, $2, $3) RETURNING id`,
		in.Code, in.Capacity, in.Kind).Scan(&in.ID); err != nil {
		if !httpx.DBError(w, err) {
			httpx.Internal(w, err)
		}
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "room.create", Entity: "room",
		EntityID: strconv.FormatInt(in.ID, 10), New: in, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, in)
}

type slotRef struct {
	Day  int `json:"day"`
	Slot int `json:"slot"`
}

// setUnavailability öğretim üyesinin müsait olmadığı saatlerin TAMAMINI değiştirir.
func (h *Handler) setUnavailability(w http.ResponseWriter, r *http.Request) {
	userID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Slots []slotRef `json:"slots"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if len(in.Slots) > 112 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "en fazla 112 saat girilebilir")
		return
	}
	for _, s := range in.Slots {
		if s.Day < 1 || s.Day > 7 || s.Slot < 0 || s.Slot > 15 {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "day 1-7, slot 0-15 olmalı")
			return
		}
	}
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var role string
	if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, userID).Scan(&role); errors.Is(err, pgx.ErrNoRows) || (err == nil && role != auth.RoleInstructor) {
		httpx.Error(w, http.StatusNotFound, "not_found", "öğretim üyesi bulunamadı")
		return
	} else if err != nil {
		httpx.Internal(w, err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM instructor_unavailable WHERE user_id = $1`, userID); err != nil {
		httpx.Internal(w, err)
		return
	}
	for _, s := range in.Slots {
		if _, err := tx.Exec(ctx,
			`INSERT INTO instructor_unavailable (user_id, day, slot) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			userID, s.Day, s.Slot); err != nil {
			httpx.Internal(w, err)
			return
		}
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "instructor.unavailability_set", Entity: "user",
		EntityID: strconv.FormatInt(userID, 10), New: in, IP: r.RemoteAddr}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"user_id": userID, "unavailable_slots": len(in.Slots)})
}

type solveRequest struct {
	Days             int   `json:"days"`
	SlotsPerDay      int   `json:"slots_per_day"`
	TimeLimitSeconds int   `json:"time_limit_seconds"`
	Seed             int64 `json:"seed"`
	Apply            bool  `json:"apply"`
}

type placementOut struct {
	SectionID  int64  `json:"section_id"`
	CourseCode string `json:"course_code"`
	Day        int    `json:"day"`
	StartMin   int    `json:"start_min"`
	EndMin     int    `json:"end_min"`
	Room       string `json:"room"`
}

// solve: POST /terms/{id}/schedule/solve — dönemin şubeleri için çakışmasız program üretir.
// apply=true ve çözüm bulunduysa program section_meetings'e yazılır (dönemin mevcut oturumları değiştirilir).
func (h *Handler) solve(w http.ResponseWriter, r *http.Request) {
	termID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	req := solveRequest{Days: 5, SlotsPerDay: 8, TimeLimitSeconds: 30}
	if r.ContentLength != 0 && !httpx.Decode(w, r, &req) {
		return
	}
	limit := time.Duration(req.TimeLimitSeconds) * time.Second
	if req.Days < 1 || req.SlotsPerDay < 1 || req.Days > 7 || req.SlotsPerDay > 16 || req.Days*req.SlotsPerDay > 64 ||
		limit < time.Second || limit > maxTimeLimit {
		httpx.Error(w, http.StatusBadRequest, "validation_error",
			"days 1-7, slots_per_day 1-16 (toplam ≤64) ve time_limit_seconds 1-60 olmalı")
		return
	}

	ctx := r.Context()
	prob, sections, err := h.buildProblem(ctx, termID, req)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "dönem bulunamadı veya şubesi yok")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}

	sctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	start := time.Now()
	res, err := scheduler.Solve(sctx, prob, scheduler.Options{Seed: req.Seed})
	if err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_problem", err.Error())
		return
	}
	out := map[string]any{
		"status": res.Status, "nodes": res.Nodes, "restarts": res.Restarts,
		"elapsed_ms": time.Since(start).Milliseconds(), "applied": false,
		"sections": len(prob.Events), "rooms": len(prob.Rooms), "instructors": len(prob.Instructors),
		"conflicts": res.Conflicts,
	}
	if res.Status != scheduler.StatusSolved {
		httpx.JSON(w, http.StatusOK, out)
		return
	}

	// Bağımsız denetçi: çözücünün çıktısını yine de doğrula (savunma amaçlı).
	if errs := scheduler.Check(prob, res.Placements); len(errs) != 0 {
		httpx.Internal(w, fmt.Errorf("çözücü geçersiz program üretti: %v", errs))
		return
	}
	places := make([]placementOut, 0, len(res.Placements))
	for _, pl := range res.Placements {
		sec := sections[pl.EventID]
		startMin := firstSlotMinute + pl.Slot*slotMinutes
		places = append(places, placementOut{
			SectionID: sec.id, CourseCode: sec.code, Day: pl.Day + 1,
			StartMin: startMin, EndMin: startMin + pl.Duration*slotMinutes, Room: strings.TrimPrefix(pl.RoomID, "R:"),
		})
	}
	out["placements"] = places
	if req.Apply {
		if err := h.apply(r, termID, places); err != nil {
			httpx.Internal(w, err)
			return
		}
		out["applied"] = true
	}
	httpx.JSON(w, http.StatusOK, out)
}

type sectionInfo struct {
	id   int64
	code string
}
