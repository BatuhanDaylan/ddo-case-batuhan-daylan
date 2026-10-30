package registration

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/batuhan-daylan/uninet/internal/db"
	"github.com/batuhan-daylan/uninet/internal/gradebook"
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
	candidateIDs = slices.Compact(slices.Sorted(slices.Values(candidateIDs)))
	codes := map[int64]string{} // ders kimliği → kod (iletilerde kullanılır)

	cands, term, err := loadCandidates(ctx, q, candidateIDs, codes)
	if err != nil {
		return loaded{}, err
	}
	current, err := loadCurrent(ctx, q, studentID, term.id, codes)
	if err != nil {
		return loaded{}, err
	}
	if err := attachMeetings(ctx, q, cands, current); err != nil {
		return loaded{}, err
	}
	passed, err := loadPassed(ctx, q, studentID)
	if err != nil {
		return loaded{}, err
	}
	// GANO, not motorundaki tek tanımdan gelir (tekrar-alma politikası ve muafiyet kuralları dahil).
	gano, err := gradebook.LoadGANO(ctx, q, studentID)
	if err != nil {
		return loaded{}, err
	}
	prereqs, err := loadPrereqs(ctx, q, cands, codes)
	if err != nil {
		return loaded{}, err
	}
	if err := fillCourseCodes(ctx, q, codes); err != nil {
		return loaded{}, err
	}
	return loaded{
		TermOpen: term.open,
		Input: rules.Input{
			Current: current, Candidates: cands, Passed: passed, GANO: gano, Prereqs: prereqs, CourseCodes: codes,
		},
	}, nil
}

type termRef struct {
	id   int64
	open bool
}

// loadCandidates aday şubeleri ve dönemlerini okur. Şubelerden biri yoksa ErrSectionNotFound,
// farklı dönemlerdeyse ErrMixedTerms döner.
func loadCandidates(ctx context.Context, q querier, ids []int64, codes map[int64]string) ([]rules.SectionInfo, termRef, error) {
	var cands []rules.SectionInfo
	var term termRef
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var si rules.SectionInfo
		var code string
		var t termRef
		if err := rows.Scan(&si.SectionID, &si.CourseID, &si.ECTS, &code, &t.id, &t.open); err != nil {
			return err
		}
		if len(cands) > 0 && t.id != term.id {
			return ErrMixedTerms
		}
		term = t
		cands = append(cands, si)
		codes[si.CourseID] = code
		return nil
	}, `SELECT s.id, s.course_id, c.ects, c.code, s.term_id, t.registration_open
	      FROM sections s JOIN courses c ON c.id = s.course_id JOIN terms t ON t.id = s.term_id
	     WHERE s.id = ANY($1)`, ids)
	if err != nil {
		return nil, term, err
	}
	if len(cands) != len(ids) {
		return nil, term, ErrSectionNotFound
	}
	return cands, term, nil
}

// loadCurrent öğrencinin bu dönemdeki aktif ve bekleyen şubelerini okur.
func loadCurrent(ctx context.Context, q querier, studentID, termID int64, codes map[int64]string) ([]rules.SectionInfo, error) {
	var current []rules.SectionInfo
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var si rules.SectionInfo
		var code string
		if err := rows.Scan(&si.SectionID, &si.CourseID, &si.ECTS, &code); err != nil {
			return err
		}
		current = append(current, si)
		codes[si.CourseID] = code
		return nil
	}, `SELECT s.id, s.course_id, c.ects, c.code
	      FROM enrollments e JOIN sections s ON s.id = e.section_id JOIN courses c ON c.id = s.course_id
	     WHERE e.student_id = $1 AND e.status IN ('enrolled', 'waitlisted') AND s.term_id = $2`, studentID, termID)
	return current, err
}

// attachMeetings aday ve mevcut şubelerin ders saatlerini tek sorguyla okuyup yerlerine yazar.
func attachMeetings(ctx context.Context, q querier, groups ...[]rules.SectionInfo) error {
	var ids []int64
	for _, g := range groups {
		for _, s := range g {
			ids = append(ids, s.SectionID)
		}
	}
	meetings := map[int64][]rules.Meeting{}
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var sid int64
		var m rules.Meeting
		if err := rows.Scan(&sid, &m.Day, &m.StartMin, &m.EndMin); err != nil {
			return err
		}
		meetings[sid] = append(meetings[sid], m)
		return nil
	}, `SELECT section_id, day, start_min, end_min FROM section_meetings WHERE section_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	for _, g := range groups {
		for i := range g {
			g[i].Meetings = meetings[g[i].SectionID]
		}
	}
	return nil
}

// loadPassed öğrencinin geçtiği dersleri döner. Denklik: Y geçildiyse "X yerine Y sayılır" kaydı olan X de geçilmiş sayılır.
func loadPassed(ctx context.Context, q querier, studentID int64) (map[int64]bool, error) {
	passed := map[int64]bool{}
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			return err
		}
		passed[cid] = true
		return nil
	}, `SELECT course_id FROM course_results WHERE student_id = $1 AND passed`, studentID)
	if err != nil || len(passed) == 0 {
		return passed, err
	}
	passedIDs := make([]int64, 0, len(passed))
	for id := range passed {
		passedIDs = append(passedIDs, id)
	}
	eq := rules.Equivalences{}
	err = db.Each(ctx, q, func(rows pgx.Rows) error {
		var course, sub int64
		if err := rows.Scan(&course, &sub); err != nil {
			return err
		}
		eq[course] = append(eq[course], sub)
		return nil
	}, `SELECT course_id, substitute_course_id FROM course_equivalences WHERE substitute_course_id = ANY($1)`, passedIDs)
	if err != nil {
		return nil, err
	}
	return rules.ExpandPassed(passed, eq), nil
}

// loadPrereqs adayların ön koşul ifadelerini okur; ifadelerde geçen dersleri codes'a (boş kodla) ekler.
func loadPrereqs(ctx context.Context, q querier, cands []rules.SectionInfo, codes map[int64]string) (map[int64]rules.Node, error) {
	courseIDs := make([]int64, 0, len(cands))
	for _, c := range cands {
		courseIDs = append(courseIDs, c.CourseID)
	}
	prereqs := map[int64]rules.Node{}
	err := db.Each(ctx, q, func(rows pgx.Rows) error {
		var cid int64
		var raw []byte
		if err := rows.Scan(&cid, &raw); err != nil {
			return err
		}
		var n rules.Node
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		prereqs[cid] = n
		for _, id := range n.Courses() {
			if _, ok := codes[id]; !ok {
				codes[id] = ""
			}
		}
		return nil
	}, `SELECT course_id, expr FROM course_prerequisites WHERE course_id = ANY($1)`, courseIDs)
	return prereqs, err
}

// fillCourseCodes kodu henüz bilinmeyen (yalnızca ön koşul ifadesinde geçen) derslerin kodlarını okur.
func fillCourseCodes(ctx context.Context, q querier, codes map[int64]string) error {
	var missing []int64
	for id, c := range codes {
		if c == "" {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return db.Each(ctx, q, func(rows pgx.Rows) error {
		var id int64
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return err
		}
		codes[id] = code
		return nil
	}, `SELECT id, code FROM courses WHERE id = ANY($1)`, missing)
}
