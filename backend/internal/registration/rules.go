package registration

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/rules"
)

var ErrMixedTerms = errors.New("şubeler aynı döneme ait olmalı")

// RuleViolationsError kayıt kurallarının bir veya birkaçının ihlal edildiğini taşır.
type RuleViolationsError struct{ Report rules.Report }

func (e *RuleViolationsError) Error() string { return "kayıt kuralları ihlal edildi" }

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type loaded struct {
	Input    rules.Input
	TermOpen bool
}

// loadInput bir öğrenci ve aday şubeler için kural girdisini veritabanından toplar.
// Aday şubeler aynı döneme ait olmalıdır.
func loadInput(ctx context.Context, q querier, studentID int64, candidateIDs []int64) (loaded, error) {
	var out loaded
	candidateIDs = slices.Compact(slices.Sorted(slices.Values(candidateIDs)))

	infos := map[int64]rules.SectionInfo{}
	codes := map[int64]string{}
	var termID int64

	rows, err := q.Query(ctx,
		`SELECT s.id, s.course_id, c.ects, c.code, s.term_id, t.registration_open
		   FROM sections s JOIN courses c ON c.id = s.course_id JOIN terms t ON t.id = s.term_id
		  WHERE s.id = ANY($1)`, candidateIDs)
	if err != nil {
		return out, err
	}
	var cands []rules.SectionInfo
	for rows.Next() {
		var si rules.SectionInfo
		var code string
		var term int64
		var open bool
		if err := rows.Scan(&si.SectionID, &si.CourseID, &si.ECTS, &code, &term, &open); err != nil {
			rows.Close()
			return out, err
		}
		if len(cands) > 0 && term != termID {
			rows.Close()
			return out, ErrMixedTerms
		}
		termID, out.TermOpen = term, open
		cands = append(cands, si)
		infos[si.SectionID] = si
		codes[si.CourseID] = code
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(cands) != len(candidateIDs) {
		return out, ErrSectionNotFound
	}

	// Öğrencinin bu dönemdeki aktif ve bekleyen şubeleri.
	rows, err = q.Query(ctx,
		`SELECT s.id, s.course_id, c.ects, c.code
		   FROM enrollments e JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id
		  WHERE e.student_id = $1 AND e.status IN ('enrolled', 'waitlisted') AND s.term_id = $2`, studentID, termID)
	if err != nil {
		return out, err
	}
	var current []rules.SectionInfo
	for rows.Next() {
		var si rules.SectionInfo
		var code string
		if err := rows.Scan(&si.SectionID, &si.CourseID, &si.ECTS, &code); err != nil {
			rows.Close()
			return out, err
		}
		current = append(current, si)
		infos[si.SectionID] = si
		codes[si.CourseID] = code
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Ders saatleri: aday + mevcut şubeler için tek sorgu.
	ids := make([]int64, 0, len(infos))
	for id := range infos {
		ids = append(ids, id)
	}
	rows, err = q.Query(ctx,
		`SELECT section_id, day, start_min, end_min FROM section_meetings WHERE section_id = ANY($1)`, ids)
	if err != nil {
		return out, err
	}
	meetings := map[int64][]rules.Meeting{}
	for rows.Next() {
		var sid int64
		var m rules.Meeting
		if err := rows.Scan(&sid, &m.Day, &m.StartMin, &m.EndMin); err != nil {
			rows.Close()
			return out, err
		}
		meetings[sid] = append(meetings[sid], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for i := range cands {
		cands[i].Meetings = meetings[cands[i].SectionID]
	}
	for i := range current {
		current[i].Meetings = meetings[current[i].SectionID]
	}

	// Geçilmiş dersler ve GANO.
	passed := map[int64]bool{}
	rows, err = q.Query(ctx, `SELECT course_id FROM course_results WHERE student_id = $1 AND passed`, studentID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return out, err
		}
		passed[cid] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Denklik: öğrenci Y'yi geçtiyse "X yerine Y sayılır" kaydı olan X de geçilmiş sayılır.
	if len(passed) > 0 {
		passedIDs := make([]int64, 0, len(passed))
		for id := range passed {
			passedIDs = append(passedIDs, id)
		}
		rows, err = q.Query(ctx,
			`SELECT course_id, substitute_course_id FROM course_equivalences WHERE substitute_course_id = ANY($1)`,
			passedIDs)
		if err != nil {
			return out, err
		}
		eq := rules.Equivalences{}
		for rows.Next() {
			var course, sub int64
			if err := rows.Scan(&course, &sub); err != nil {
				rows.Close()
				return out, err
			}
			eq[course] = append(eq[course], sub)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
		passed = rules.ExpandPassed(passed, eq)
	}

	// Muaf dersler notsuzdur; GANO'ya girmez.
	var gano float64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.grade_point * c.ects) / NULLIF(SUM(c.ects), 0), 0)::float8
		   FROM course_results r JOIN courses c ON c.id = r.course_id
		  WHERE r.student_id = $1 AND NOT r.exempt`,
		studentID).Scan(&gano); err != nil {
		return out, err
	}

	// Adayların ön koşulları ve ifadelerde geçen derslerin kodları.
	candCourses := make([]int64, 0, len(cands))
	for _, c := range cands {
		candCourses = append(candCourses, c.CourseID)
	}
	prereqs := map[int64]rules.Node{}
	rows, err = q.Query(ctx, `SELECT course_id, expr FROM course_prerequisites WHERE course_id = ANY($1)`, candCourses)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var cid int64
		var raw []byte
		if err := rows.Scan(&cid, &raw); err != nil {
			rows.Close()
			return out, err
		}
		var n rules.Node
		if err := json.Unmarshal(raw, &n); err != nil {
			rows.Close()
			return out, err
		}
		prereqs[cid] = n
		for _, id := range n.Courses() {
			if _, ok := codes[id]; !ok {
				codes[id] = ""
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	var missing []int64
	for id, c := range codes {
		if c == "" {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		rows, err = q.Query(ctx, `SELECT id, code FROM courses WHERE id = ANY($1)`, missing)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id int64
			var code string
			if err := rows.Scan(&id, &code); err != nil {
				rows.Close()
				return out, err
			}
			codes[id] = code
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}

	out.Input = rules.Input{
		Current: current, Candidates: cands, Passed: passed, GANO: gano, Prereqs: prereqs, CourseCodes: codes,
	}
	return out, nil
}
