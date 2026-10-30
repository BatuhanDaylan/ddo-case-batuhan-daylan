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
	"github.com/batuhan-daylan/uninet/internal/db"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/scheduler"
)

// buildProblem dönemin şubelerini ve derslik/müsaitlik verisini çözücü problemine çevirir.
// Şube yoksa pgx.ErrNoRows döner.
func (h *Handler) buildProblem(ctx context.Context, termID int64, req solveRequest) (scheduler.Problem, map[string]sectionInfo, error) {
	prob := scheduler.Problem{Config: scheduler.Config{Days: req.Days, SlotsPerDay: req.SlotsPerDay}}

	groups, err := loadSemesterGroups(ctx, h.pool)
	if err != nil {
		return prob, nil, err
	}
	events, sections, instructors, err := loadEvents(ctx, h.pool, termID, req.SlotsPerDay, groups)
	if err != nil {
		return prob, nil, err
	}
	if len(events) == 0 {
		return prob, nil, pgx.ErrNoRows
	}
	prob.Events = events
	if prob.Rooms, err = loadRooms(ctx, h.pool); err != nil {
		return prob, nil, err
	}
	// Müsaitlikler önce bir haritaya toplanır; öğretim üyeleri sonra oluşturulur. (Dilim elemanlarına
	// işaretçi tutmak, dilim büyüyünce eski diziye işaret eder ve müsaitlik sessizce kaybolur.)
	unavailable, err := loadUnavailability(ctx, h.pool, instructors, req)
	if err != nil {
		return prob, nil, err
	}
	for _, id := range instructors {
		prob.Instructors = append(prob.Instructors, scheduler.Instructor{
			ID: "U" + strconv.FormatInt(id, 10), Unavailable: unavailable[id],
		})
	}
	return prob, sections, nil
}

// loadSemesterGroups zorunlu derslerin yarıyıl gruplarını döner (ders → grup adları): aynı müfredat sürümü +
// yarıyıl = öğrenci aynı anda alır, çakışamaz.
func loadSemesterGroups(ctx context.Context, q db.Querier) (map[int64][]string, error) {
	groups := map[int64][]string{}
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var cid, vid int64
		var sem int
		if err := rows.Scan(&cid, &vid, &sem); err != nil {
			return err
		}
		groups[cid] = append(groups[cid], fmt.Sprintf("v%d-yariyil%d", vid, sem))
		return nil
	}, `SELECT course_id, curriculum_version_id, semester FROM curriculum_courses WHERE kind = 'required'`)
	return groups, err
}

// loadEvents dönemin şubelerini çözücü etkinliklerine çevirir. Etkinlik kimliği "S<şube>"; öğretim üyeleri
// ilk görülme sırasıyla ayrıca döner.
func loadEvents(ctx context.Context, q db.Querier, termID int64, slotsPerDay int, groups map[int64][]string) ([]scheduler.Event, map[string]sectionInfo, []int64, error) {
	var events []scheduler.Event
	sections := map[string]sectionInfo{}
	instructors := []int64{}
	seen := map[int64]bool{}
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var sid, cid int64
		var code, kind string
		var instr *int64
		var capacity, hours int
		if err := rows.Scan(&sid, &cid, &code, &instr, &capacity, &hours, &kind); err != nil {
			return err
		}
		ev := scheduler.Event{
			ID: "S" + strconv.FormatInt(sid, 10), Name: code, Students: capacity, Kind: kind,
			Duration: min(hours, slotsPerDay), Groups: groups[cid],
		}
		if instr != nil {
			ev.Instructor = "U" + strconv.FormatInt(*instr, 10)
			if !seen[*instr] {
				seen[*instr] = true
				instructors = append(instructors, *instr)
			}
		}
		events = append(events, ev)
		sections[ev.ID] = sectionInfo{id: sid, code: code}
		return nil
	}, `SELECT s.id, s.course_id, c.code, s.instructor_id, s.capacity, c.weekly_hours, c.room_kind
	      FROM sections s JOIN courses c ON c.id = s.course_id WHERE s.term_id = $1 ORDER BY s.id`, termID)
	return events, sections, instructors, err
}

// loadRooms derslikleri çözücü biçiminde döner (kimlik "R:<kod>").
func loadRooms(ctx context.Context, q db.Querier) ([]scheduler.Room, error) {
	var rooms []scheduler.Room
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var rm scheduler.Room
		var code string
		if err := rows.Scan(&code, &rm.Capacity, &rm.Kind); err != nil {
			return err
		}
		rm.ID = "R:" + code
		rooms = append(rooms, rm)
		return nil
	}, `SELECT code, capacity, kind FROM rooms ORDER BY id`)
	return rooms, err
}

// loadUnavailability öğretim üyelerinin müsait olmadığı saatleri döner (kullanıcı → gün*SlotsPerDay + saat).
func loadUnavailability(ctx context.Context, q db.Querier, instructors []int64, req solveRequest) (map[int64][]int, error) {
	unavailable := map[int64][]int{}
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var uid int64
		var day, slot int
		if err := rows.Scan(&uid, &day, &slot); err != nil {
			return err
		}
		if day <= req.Days && slot < req.SlotsPerDay { // ızgara dışındaki saatler zaten kullanılmaz
			unavailable[uid] = append(unavailable[uid], (day-1)*req.SlotsPerDay+slot)
		}
		return nil
	}, `SELECT user_id, day, slot FROM instructor_unavailable WHERE user_id = ANY($1)`, instructors)
	return unavailable, err
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
