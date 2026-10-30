package timetable

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/scheduler"
)

// buildProblem dönemin şubelerini ve derslik/müsaitlik verisini çözücü problemine çevirir.
// Şube yoksa pgx.ErrNoRows döner.
func (h *Handler) buildProblem(ctx context.Context, termID int64, req solveRequest) (scheduler.Problem, map[string]sectionInfo, error) {
	prob := scheduler.Problem{Config: scheduler.Config{Days: req.Days, SlotsPerDay: req.SlotsPerDay}}
	sections := map[string]sectionInfo{}

	// Zorunlu derslerin yarıyıl grupları: aynı müfredat sürümü + yarıyıl = öğrenci aynı anda alır, çakışamaz.
	groups := map[int64][]string{}
	grows, err := h.pool.Query(ctx,
		`SELECT course_id, curriculum_version_id, semester FROM curriculum_courses WHERE kind = 'required'`)
	if err != nil {
		return prob, nil, err
	}
	for grows.Next() {
		var cid, vid int64
		var sem int
		if err := grows.Scan(&cid, &vid, &sem); err != nil {
			grows.Close()
			return prob, nil, err
		}
		groups[cid] = append(groups[cid], fmt.Sprintf("v%d-yariyil%d", vid, sem))
	}
	grows.Close()
	if err := grows.Err(); err != nil {
		return prob, nil, err
	}

	rows, err := h.pool.Query(ctx,
		`SELECT s.id, s.course_id, c.code, s.instructor_id, s.capacity, c.weekly_hours, c.room_kind
		   FROM sections s JOIN courses c ON c.id = s.course_id WHERE s.term_id = $1 ORDER BY s.id`, termID)
	if err != nil {
		return prob, nil, err
	}
	instructors := []int64{}
	seenInstr := map[int64]bool{}
	for rows.Next() {
		var sid, cid int64
		var code, kind string
		var instr *int64
		var capacity, hours int
		if err := rows.Scan(&sid, &cid, &code, &instr, &capacity, &hours, &kind); err != nil {
			rows.Close()
			return prob, nil, err
		}
		ev := scheduler.Event{
			ID: "S" + strconv.FormatInt(sid, 10), Name: code, Students: capacity, Kind: kind,
			Duration: min(hours, req.SlotsPerDay), Groups: groups[cid],
		}
		if instr != nil {
			ev.Instructor = "U" + strconv.FormatInt(*instr, 10)
			if !seenInstr[*instr] {
				seenInstr[*instr] = true
				instructors = append(instructors, *instr)
			}
		}
		prob.Events = append(prob.Events, ev)
		sections[ev.ID] = sectionInfo{id: sid, code: code}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return prob, nil, err
	}
	if len(prob.Events) == 0 {
		return prob, nil, pgx.ErrNoRows
	}

	rrows, err := h.pool.Query(ctx, `SELECT code, capacity, kind FROM rooms ORDER BY id`)
	if err != nil {
		return prob, nil, err
	}
	for rrows.Next() {
		var rm scheduler.Room
		var code string
		if err := rrows.Scan(&code, &rm.Capacity, &rm.Kind); err != nil {
			rrows.Close()
			return prob, nil, err
		}
		rm.ID = "R:" + code
		prob.Rooms = append(prob.Rooms, rm)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return prob, nil, err
	}

	// Müsaitlikler önce bir haritaya toplanır; öğretim üyeleri sonra oluşturulur. (Dilim elemanlarına
	// işaretçi tutmak, dilim büyüyünce eski diziye işaret eder ve müsaitlik sessizce kaybolur.)
	unavailable := map[int64][]int{}
	urows, err := h.pool.Query(ctx,
		`SELECT user_id, day, slot FROM instructor_unavailable WHERE user_id = ANY($1)`, instructors)
	if err != nil {
		return prob, nil, err
	}
	for urows.Next() {
		var uid int64
		var day, slot int
		if err := urows.Scan(&uid, &day, &slot); err != nil {
			urows.Close()
			return prob, nil, err
		}
		if day <= req.Days && slot < req.SlotsPerDay { // ızgara dışındaki saatler zaten kullanılmaz
			unavailable[uid] = append(unavailable[uid], (day-1)*req.SlotsPerDay+slot)
		}
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return prob, nil, err
	}
	for _, id := range instructors {
		prob.Instructors = append(prob.Instructors, scheduler.Instructor{
			ID: "U" + strconv.FormatInt(id, 10), Unavailable: unavailable[id],
		})
	}
	return prob, sections, nil
}

// apply programı section_meetings'e yazar: dönemin mevcut oturumları yenisiyle değiştirilir.
func (h *Handler) apply(r *http.Request, termID int64, places []placementOut) error {
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`DELETE FROM section_meetings WHERE section_id IN (SELECT id FROM sections WHERE term_id = $1)`, termID); err != nil {
		return err
	}
	for _, pl := range places {
		if _, err := tx.Exec(ctx,
			`INSERT INTO section_meetings (section_id, day, start_min, end_min, room_id)
			 VALUES ($1, $2, $3, $4, (SELECT id FROM rooms WHERE code = $5))`,
			pl.SectionID, pl.Day, pl.StartMin, pl.EndMin, pl.Room); err != nil {
			return err
		}
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "term.schedule_applied", Entity: "term",
		EntityID: strconv.FormatInt(termID, 10), New: map[string]int{"sections": len(places)}, IP: r.RemoteAddr}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type scheduleRow struct {
	ID         int64  `json:"id"`
	SectionID  int64  `json:"section_id"`
	CourseCode string `json:"course_code"`
	Day        int    `json:"day"`
	StartMin   int    `json:"start_min"`
	EndMin     int    `json:"end_min"`
	Room       string `json:"room,omitempty"`
}

// getSchedule: GET /terms/{id}/schedule — dönemin ders programı (keyset sayfalı).
func (h *Handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	termID, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	after, limit, ok := httpx.PageParams(r)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "after/limit geçersiz")
		return
	}
	rows, err := h.pool.Query(r.Context(),
		`SELECT m.id, s.id, c.code, m.day, m.start_min, m.end_min, COALESCE(rm.code, '')
		   FROM section_meetings m JOIN sections s ON s.id = m.section_id JOIN courses c ON c.id = s.course_id
		   LEFT JOIN rooms rm ON rm.id = m.room_id
		  WHERE s.term_id = $1 AND m.id > $2 ORDER BY m.id LIMIT $3`, termID, after, limit+1)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer rows.Close()
	var items []scheduleRow
	for rows.Next() {
		var x scheduleRow
		if err := rows.Scan(&x.ID, &x.SectionID, &x.CourseCode, &x.Day, &x.StartMin, &x.EndMin, &x.Room); err != nil {
			httpx.Internal(w, err)
			return
		}
		items = append(items, x)
	}
	httpx.JSON(w, http.StatusOK, httpx.NewPage(items, limit, func(x scheduleRow) int64 { return x.ID }))
}

// SeedDemo örnek derslikleri ve ders ihtiyaçlarını kurar. İdempotenttir.
func SeedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx,
		`INSERT INTO rooms (code, capacity, kind) VALUES
		   ('SINIF-101', 50, 'lecture'), ('SINIF-102', 50, 'lecture'), ('SINIF-103', 60, 'lecture'),
		   ('LAB-1', 40, 'lab'), ('LAB-2', 40, 'lab'), ('AMFI-1', 150, 'amphi')
		 ON CONFLICT (code) DO NOTHING`); err != nil {
		return err
	}
	// Programlama dersleri laboratuvarda; BM101 haftada 3 saat.
	_, err := pool.Exec(ctx,
		`UPDATE courses SET room_kind = 'lab' WHERE code IN ('BM101', 'BM102');
		 UPDATE courses SET weekly_hours = 3 WHERE code = 'BM101'`)
	return err
}
