package timetable

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/auth"
	"github.com/batuhan-daylan/uninet/internal/httpx"
	"github.com/batuhan-daylan/uninet/internal/scheduler"
)

// Elle düzenleme ızgarası çözücünün varsayılanıyla aynıdır (5 gün × 8 saat, 09:00 başlangıç).
const (
	gridDays  = 5
	gridSlots = 8
)

type moveRequest struct {
	Day      int    `json:"day"`       // 1=Pzt
	StartMin int    `json:"start_min"` // ızgaraya hizalı: 09:00 + n*60
	Room     string `json:"room"`      // boşsa mevcut derslik korunur
	DryRun   bool   `json:"dry_run"`   // true: yalnızca ihlalleri hesapla, kaydetme
}

var idToken = regexp.MustCompile(`\bS\d+\b|R:[^\s]+`)

// moveMeeting: PATCH /section-meetings/{id}
// Bir ders oturumunu başka gün/saate (ve isteğe bağlı derslik) taşır. Çözücünün bağımsız denetçisi (scheduler.Check)
// önce ve sonra çalıştırılır; yalnızca TAŞIMANIN YENİ OLUŞTURDUĞU ihlaller raporlanır (hepsi birlikte). Önceden var olan
// ihlaller taşımayı engellemez. İhlal varsa 409 ve hiçbir şey yazılmaz; dry_run=true her durumda yazmaz.
func (h *Handler) moveMeeting(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	var req moveRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	slot := (req.StartMin - firstSlotMinute) / slotMinutes
	if req.Day < 1 || req.Day > gridDays || req.StartMin < firstSlotMinute || (req.StartMin-firstSlotMinute)%slotMinutes != 0 || slot >= gridSlots {
		httpx.Error(w, http.StatusBadRequest, "validation_error",
			fmt.Sprintf("day 1-%d ve start_min 09:00'dan başlayarak saat başı (540 + n*60, n<%d) olmalı", gridDays, gridSlots))
		return
	}

	ctx := r.Context()
	var sectionID, termID int64
	var curStart, curEnd int
	var curRoom string
	err := h.pool.QueryRow(ctx,
		`SELECT m.section_id, s.term_id, m.start_min, m.end_min, COALESCE(rm.code, '')
		   FROM section_meetings m JOIN sections s ON s.id = m.section_id LEFT JOIN rooms rm ON rm.id = m.room_id
		  WHERE m.id = $1`, id).Scan(&sectionID, &termID, &curStart, &curEnd, &curRoom)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "ders oturumu bulunamadı")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	room := req.Room
	if room == "" {
		room = curRoom
	}
	duration := (curEnd - curStart) / slotMinutes
	if (curEnd-curStart)%slotMinutes != 0 || curStart < firstSlotMinute || room == "" || req.StartMin+duration*slotMinutes > firstSlotMinute+gridSlots*slotMinutes {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "oturum ızgara dışına taşıyor ya da derslik belirtilmemiş")
		return
	}

	conflicts, err := h.moveConflicts(ctx, termID, sectionID, "R:"+room, req.Day-1, slot, duration)
	if errors.Is(err, errNotEditable) {
		httpx.Error(w, http.StatusConflict, "not_editable", "bu şubenin birden fazla oturumu var ya da program ızgarasına uymuyor; çözücüyü kullanın")
		return
	}
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	if len(conflicts) > 0 {
		httpx.JSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
			"code": "schedule_conflict", "message": "taşıma yeni çakışmalar oluşturuyor", "conflicts": conflicts}})
		return
	}
	if req.DryRun {
		httpx.JSON(w, http.StatusOK, map[string]any{"valid": true, "saved": false})
		return
	}

	p, _ := auth.PrincipalFrom(ctx)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Internal(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	newEnd := req.StartMin + duration*slotMinutes
	if _, err := tx.Exec(ctx,
		`UPDATE section_meetings SET day = $2, start_min = $3, end_min = $4, room_id = (SELECT id FROM rooms WHERE code = $5) WHERE id = $1`,
		id, req.Day, req.StartMin, newEnd, room); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorID: &p.UserID, Action: "section_meeting.moved", Entity: "section_meeting",
		EntityID: strconv.FormatInt(id, 10), IP: r.RemoteAddr,
		Old: map[string]any{"start_min": curStart, "end_min": curEnd, "room": curRoom},
		New: map[string]any{"day": req.Day, "start_min": req.StartMin, "end_min": newEnd, "room": room}}); err != nil {
		httpx.Internal(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Internal(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": true, "saved": true})
}

var errNotEditable = errors.New("not editable")

// moveConflicts taşımanın yeni oluşturduğu ihlalleri insan okunur biçimde döner (boş = temiz).
func (h *Handler) moveConflicts(ctx context.Context, termID, sectionID int64, roomID string, day, slot, duration int) ([]string, error) {
	prob, sections, err := h.buildProblem(ctx, termID, solveRequest{Days: gridDays, SlotsPerDay: gridSlots})
	if err != nil {
		return nil, err
	}
	rows, err := h.pool.Query(ctx,
		`SELECT m.section_id, m.day, m.start_min, m.end_min, COALESCE(rm.code, '')
		   FROM section_meetings m JOIN sections s ON s.id = m.section_id LEFT JOIN rooms rm ON rm.id = m.room_id
		  WHERE s.term_id = $1`, termID)
	if err != nil {
		return nil, err
	}
	type mt struct {
		day, start, end int
		room            string
	}
	bySection := map[int64][]mt{}
	for rows.Next() {
		var sid int64
		var m mt
		if err := rows.Scan(&sid, &m.day, &m.start, &m.end, &m.room); err != nil {
			rows.Close()
			return nil, err
		}
		bySection[sid] = append(bySection[sid], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Yalnızca tek oturumlu, ızgaraya oturan şubeler değerlendirilir.
	var placements []scheduler.Placement
	events := prob.Events[:0:0]
	moved := -1
	for _, e := range prob.Events {
		sid := sections[e.ID].id
		ms := bySection[sid]
		if len(ms) != 1 || ms[0].room == "" || ms[0].day < 1 || ms[0].day > gridDays ||
			ms[0].start < firstSlotMinute || (ms[0].start-firstSlotMinute)%slotMinutes != 0 || (ms[0].end-ms[0].start)%slotMinutes != 0 {
			if sid == sectionID {
				return nil, errNotEditable
			}
			continue
		}
		pl := scheduler.Placement{EventID: e.ID, Day: ms[0].day - 1, Slot: (ms[0].start - firstSlotMinute) / slotMinutes,
			Duration: (ms[0].end - ms[0].start) / slotMinutes, RoomID: "R:" + ms[0].room}
		if sid == sectionID {
			moved = len(placements)
		}
		events = append(events, e)
		placements = append(placements, pl)
	}
	if moved < 0 {
		return nil, errNotEditable
	}
	prob.Events = events

	before := scheduler.Check(prob, placements)
	placements[moved].Day, placements[moved].Slot, placements[moved].Duration, placements[moved].RoomID = day, slot, duration, roomID
	after := scheduler.Check(prob, placements)

	names := map[string]string{}
	for k, v := range sections {
		names[k] = v.code
	}
	var out []string
	for _, msg := range after {
		if slices.Contains(before, msg) {
			continue
		}
		out = append(out, idToken.ReplaceAllStringFunc(msg, func(t string) string {
			if strings.HasPrefix(t, "R:") {
				return t[2:]
			}
			if n, ok := names[t]; ok {
				return n
			}
			return t
		}))
	}
	return out, nil
}
