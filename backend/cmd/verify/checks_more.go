package main

import (
	"bytes"
	"fmt"
	"strings"
)

func (e *env) code(c string) string { return "V" + c + e.sfx }

func chkF4(t *T, e *env) {
	w := e.world()
	owner := map[string]user{"A": w.instrA, "B": w.instrB, "C": w.instrA, "D": w.instrB, "E": w.instrA}
	for _, c := range []string{"B", "C", "D", "E"} {
		r := e.call("POST", "/sections", e.reg, M{"term_id": w.term, "course_id": w.course[c], "code": "01", "instructor_id": owner[c].id, "capacity": 40})
		t.must(r.Status == 201, "şube %s: %d %s", c, r.Status, r.Raw)
		w.sections[c] = r.id("id")
	}

	// Haftalık ders programı: çözücü çakışmasız program üretir ve uygular.
	r := e.call("POST", p("/terms/%d/schedule/solve", w.term), e.reg, M{"apply": true, "time_limit_seconds": 20})
	t.must(r.Status == 200 && r.str("status") == "solved" && r.boolean("applied"), "çözüm: %d %s", r.Status, r.Raw)
	type pl struct {
		sec           int64
		code, room    string
		day, from, to int
	}
	var places []pl
	for _, x := range r.arr("placements") {
		m := x.(M)
		places = append(places, pl{int64(m["section_id"].(float64)), m["course_code"].(string), m["room"].(string),
			int(m["day"].(float64)), int(m["start_min"].(float64)), int(m["end_min"].(float64))})
	}
	t.must(len(places) == 5, "yerleşen şube sayısı %d, beklenen 5", len(places))
	instrOf := map[int64]int64{}
	for c, u := range owner {
		instrOf[w.sections[c]] = u.id
	}
	overlap := func(a, b pl) bool { return a.day == b.day && a.from < b.to && b.from < a.to }
	groupPair := map[[2]string]bool{{e.code("A"), e.code("B")}: true, {e.code("C"), e.code("D")}: true}
	for i := range places {
		for j := i + 1; j < len(places); j++ {
			a, b := places[i], places[j]
			if !overlap(a, b) {
				continue
			}
			t.soft(a.room != b.room, "%s ve %s aynı derslikte çakışıyor", a.code, b.code)
			t.soft(instrOf[a.sec] != instrOf[b.sec], "%s ve %s aynı öğretim üyesinde çakışıyor", a.code, b.code)
			t.soft(!groupPair[[2]string{a.code, b.code}] && !groupPair[[2]string{b.code, a.code}], "aynı yarıyılın zorunlu dersleri %s ve %s çakışıyor", a.code, b.code)
		}
	}
	if len(t.errs) > 0 {
		panic(abort{})
	}

	// Elle düzenleme (sürükle-bırak arka ucu): aynı yarıyılın zorunlu dersinin saatine taşıma reddedilir ve hiçbir şey değişmez;
	// boş bir saate taşıma kaydedilir; öğrenci taşıyamaz.
	all := e.call("GET", p("/terms/%d/schedule?limit=50", w.term), e.stu, nil)
	meet := map[int64]M{}
	for _, x := range all.arr("items") {
		m := x.(M)
		meet[int64(m["section_id"].(float64))] = m
	}
	a, b := meet[w.sections["A"]], meet[w.sections["B"]]
	t.must(a != nil && b != nil, "A/B oturumları programda yok")
	bURL := p("/section-meetings/%d", int64(b["id"].(float64)))
	conflict := e.call("PATCH", bURL, e.reg, M{"day": a["day"], "start_min": a["start_min"]})
	t.must(conflict.Status == 409 && conflict.str("error.code") == "schedule_conflict" && len(conflict.arr("error.conflicts")) > 0,
		"zorunlu ders çakışması reddedilmedi: %d %s", conflict.Status, conflict.Raw)
	t.must(e.call("PATCH", bURL, e.stu, M{"day": 1, "start_min": 540}).Status == 403, "öğrenci oturum taşıdı")
	moved := false
	for d := 1; d <= 5 && !moved; d++ {
		for s := 540; s <= 540+6*60 && !moved; s += 60 {
			if d == int(b["day"].(float64)) && s == int(b["start_min"].(float64)) {
				continue
			}
			if e.call("PATCH", bURL, e.reg, M{"day": d, "start_min": s, "dry_run": true}).Status != 200 {
				continue
			}
			r := e.call("PATCH", bURL, e.reg, M{"day": d, "start_min": s})
			t.must(r.Status == 200 && r.boolean("saved"), "geçerli taşıma kaydedilmedi: %d %s", r.Status, r.Raw)
			moved = true
			after := e.call("GET", p("/terms/%d/schedule?limit=50", w.term), e.stu, nil)
			for _, x := range after.arr("items") {
				if m := x.(M); int64(m["id"].(float64)) == int64(b["id"].(float64)) {
					t.must(int(m["day"].(float64)) == d && int(m["start_min"].(float64)) == s, "taşıma programa yansımadı")
				}
			}
		}
	}
	t.must(moved, "taşınabilecek boş saat bulunamadı")

	// Görüntüleme: sayfalı program.
	pg := e.call("GET", p("/terms/%d/schedule?limit=2", w.term), e.stu, nil)
	t.must(pg.Status == 200 && len(pg.arr("items")) == 2 && pg.val("next_after") != nil, "program sayfası: %s", pg.Raw)

	// Sınav takvimi: oda, öğretim üyesi çakışması reddedilir; hepsi birlikte listelenir.
	exam := func(c string, start, room string) R {
		body := M{"kind": "midterm", "starts_at": start, "duration_min": 90}
		if room != "" {
			body["room"] = room
		}
		return e.call("POST", p("/sections/%d/exams", w.sections[c]), e.reg, body)
	}
	room := "V1" + e.sfx
	t.must(exam("A", "2031-01-12T09:00:00Z", room).Status == 201, "ilk sınav")
	c1 := exam("B", "2031-01-12T09:30:00Z", room) // aynı oda, çakışan saat
	t.must(c1.Status == 409 && containsStr(c1.listField("error.conflicts", "kind"), "ROOM"), "oda çakışması reddedilmedi: %d %s", c1.Status, c1.Raw)
	c2 := exam("C", "2031-01-12T10:00:00Z", "V2"+e.sfx) // C'nin öğretim üyesi A ile aynı → INSTRUCTOR
	t.must(c2.Status == 409 && containsStr(c2.listField("error.conflicts", "kind"), "INSTRUCTOR"), "öğretim üyesi çakışması reddedilmedi: %d %s", c2.Status, c2.Raw)
	t.must(exam("B", "2031-01-12T10:30:00Z", room).Status == 201, "bitişik/çakışmayan sınav reddedildi")
	t.must(e.call("POST", p("/sections/%d/exams", w.sections["A"]), e.stu, M{"kind": "final", "starts_at": "2031-02-01T09:00:00Z", "duration_min": 60}).Status == 403, "öğrenci sınav girdi")
	list := e.call("GET", p("/terms/%d/exams", w.term), e.stu, nil)
	t.must(list.Status == 200 && len(list.arr("items")) == 2, "sınav takvimi: %s", list.Raw)
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func chkF5(t *T, e *env) {
	w := e.world()
	secC := w.sections["C"]
	var enr []int64
	for i := 0; i < 4; i++ {
		s := e.newStudent(t, p("f5-%d", i), w.prog, 2021, nil)
		w.gs = append(w.gs, s)
		r := e.enroll(s.token, secC, p("f5-%s-%d", e.sfx, i))
		t.must(r.Status == 201 && r.str("status") == "enrolled", "kayıt %d: %s", i, r.Raw)
		enr = append(enr, r.id("enrollment_id"))
	}
	ia := w.instrA.token

	comp := e.call("PUT", p("/sections/%d/grade-components", secC), ia, M{"method": "absolute",
		"components": []M{{"name": "midterm", "weight": 40}, {"name": "final", "weight": 60}}})
	t.must(comp.Status == 200, "bileşenler: %d %s", comp.Status, comp.Raw)
	t.must(e.call("PUT", p("/sections/%d/grade-components", secC), ia, M{"method": "absolute",
		"components": []M{{"name": "midterm", "weight": 30}, {"name": "final", "weight": 60}}}).Status == 400, "toplamı 100 olmayan ağırlıklar kabul edildi")

	sc := func(i int, comp, kind string, v float64) M {
		return M{"enrollment_id": enr[i], "component": comp, "kind": kind, "score": v}
	}
	put := e.call("PUT", p("/sections/%d/scores", secC), ia, M{"entries": []M{
		sc(0, "midterm", "regular", 90), sc(0, "final", "regular", 90), // 90 → AA
		sc(1, "midterm", "regular", 80), sc(1, "final", "regular", 20), sc(1, "final", "makeup", 80), // bütünleme: 80 → BB
		sc(2, "midterm", "regular", 50), sc(2, "final", "regular", 50), // 50 → DD
		sc(3, "midterm", "regular", 30), sc(3, "final", "regular", 30), // 30 → FF
	}})
	t.must(put.Status == 200, "puan girişi: %d %s", put.Status, put.Raw)
	t.must(e.call("PUT", p("/sections/%d/scores", secC), w.instrB.token, M{"entries": []M{sc(0, "midterm", "regular", 1)}}).Status == 403, "başka öğretim üyesi puan girdi")

	pub := e.call("POST", p("/sections/%d/grades/publish", secC), ia, nil)
	t.must(pub.Status == 200, "ilan: %d %s", pub.Status, pub.Raw)
	letters := map[int64]string{}
	w.gradeResult = map[int64]int64{}
	for _, x := range pub.arr("results") {
		m := x.(M)
		letters[int64(m["student_id"].(float64))] = m["letter"].(string)
		w.gradeResult[int64(m["student_id"].(float64))] = int64(m["result_id"].(float64))
	}
	for i, want := range []string{"AA", "BB", "DD", "FF"} {
		t.must(letters[w.gs[i].sid] == want, "öğrenci %d notu %s, beklenen %s (bütünleme önceliği/ağırlık hesabı)", i, letters[w.gs[i].sid], want)
	}
	t.must(e.call("POST", p("/sections/%d/grades/publish", secC), ia, nil).Status == 409, "ikinci ilan kabul edildi")

	// İtiraz: öğrenci 2 (DD) itiraz eder; öğretim üyesi kabul eder; not ikinci kişi onaylayana kadar değişmez.
	res2 := w.gradeResult[w.gs[2].sid]
	t.must(e.call("POST", p("/course-results/%d/objections", res2), w.gs[3].token, M{"reason": "x"}).Status == 404, "başkasının sonucuna itiraz edilebildi")
	ob := e.call("POST", p("/course-results/%d/objections", res2), w.gs[2].token, M{"reason": "Final kâğıdım yanlış değerlendirildi"})
	t.must(ob.Status == 201, "itiraz: %d %s", ob.Status, ob.Raw)
	acc := e.call("POST", p("/grade-objections/%d/decision", ob.id("id")), ia, M{"accept": true, "new_letter": "CC", "response": "Toplama hatası doğrulandı"})
	t.must(acc.Status == 200 && acc.str("status") == "accepted" && acc.val("correction_id") != nil, "itiraz kabulü: %d %s", acc.Status, acc.Raw)
	cur := e.call("GET", p("/students/%d/results", w.gs[2].sid), w.gs[2].token, nil)
	t.must(cur.arr("results")[0].(M)["letter"] == "DD", "itiraz kabulü notu tek başına değiştirdi")
	dec := e.call("POST", p("/grade-corrections/%d/decision", acc.id("correction_id")), e.sec, M{"approve": true})
	t.must(dec.Status == 200, "düzeltme onayı: %d %s", dec.Status, dec.Raw)
	cur = e.call("GET", p("/students/%d/results", w.gs[2].sid), w.gs[2].token, nil)
	t.must(cur.arr("results")[0].(M)["letter"] == "CC", "onaydan sonra not CC olmalı: %s", cur.Raw)
}

func chkF6(t *T, e *env) {
	w := e.world()
	st := w.gs[0]
	iss := e.call("POST", p("/students/%d/transcripts", st.sid), st.token, nil)
	t.must(iss.Status == 201 && iss.str("code") != "", "belge düzenleme: %d %s", iss.Status, iss.Raw)
	code := iss.str("code")

	// İdempotans: aynı Idempotency-Key ile tekrar istek yeni belge üretmez, ilk yanıtı döner.
	key := "verify-transcript-" + e.sfx
	k1 := e.call("POST", p("/students/%d/transcripts", st.sid), st.token, nil, "Idempotency-Key", key)
	k2 := e.call("POST", p("/students/%d/transcripts", st.sid), st.token, nil, "Idempotency-Key", key)
	t.must(k1.Status == 201 && k2.Status == 201 && k1.id("id") == k2.id("id") && k1.str("code") == k2.str("code") && k2.Header.Get("Idempotent-Replayed") == "true",
		"aynı anahtar aynı belgeyi dönmedi: %s | %s", k1.Raw, k2.Raw)

	pdf := e.call("GET", iss.str("pdf_url"), st.token, nil)
	t.must(pdf.Status == 200 && strings.HasPrefix(pdf.Header.Get("Content-Type"), "application/pdf"), "pdf: %d %s", pdf.Status, pdf.Header.Get("Content-Type"))
	t.must(bytes.HasPrefix(pdf.Raw, []byte("%PDF-")) && len(pdf.Raw) > 5000, "geçerli PDF değil (%d bayt)", len(pdf.Raw))

	// Doğrulama herkese açıktır (kimlik bilgisi YOK).
	v := e.call("GET", "/verify/"+code, "", nil)
	t.must(v.Status == 200 && v.boolean("valid") && v.num("course_count") == 1 && strings.Contains(v.str("full_name"), "Doğrulama"), "doğrulama: %d %s", v.Status, v.Raw)
	// Sahte/bozuk kod aynı 404'ü verir.
	bad := code[:len(code)-1] + map[bool]string{true: "A", false: "B"}[code[len(code)-1] != 'A']
	t.must(e.call("GET", "/verify/"+bad, "", nil).Status == 404, "bozulmuş kod doğrulandı")
	t.must(e.call("GET", "/verify/9999999.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "", nil).Status == 404, "olmayan belge doğrulandı")
	// Yetki.
	t.must(e.call("GET", iss.str("pdf_url"), w.gs[1].token, nil).Status == 403, "başka öğrenci PDF indirebildi")
	t.must(e.call("GET", iss.str("pdf_url"), "", nil).Status == 401, "kimliksiz PDF indirilebildi")
	t.must(e.call("POST", p("/students/%d/transcripts", st.sid), w.gs[1].token, nil).Status == 403, "başkası adına belge düzenlendi")
}

func chkF7(t *T, e *env) {
	w := e.world()
	term0 := e.call("POST", "/terms", e.reg, M{"code": "VO" + e.sfx, "name": "Geçmiş Dönem", "registration_open": false})
	t.must(term0.Status == 201, "geçmiş dönem: %s", term0.Raw)
	w.term0 = term0.id("id")

	rules := M{"min_gano": 0, "min_total_ects": 0, "pools": []M{
		{"name": "P1", "courses": []string{e.code("E"), e.code("F")}, "min_courses": 1},
		{"name": "P2", "courses": []string{e.code("F"), e.code("G")}, "min_courses": 1},
	}}
	t.must(e.call("PUT", p("/curriculum-versions/%d/graduation-rules", w.v2020), e.stu, rules).Status == 403, "öğrenci mezuniyet kuralı yazdı")
	t.must(e.call("PUT", p("/curriculum-versions/%d/graduation-rules", w.v2020), e.reg, M{"min_gano": 9}).Status == 400, "geçersiz kural kabul edildi")
	t.must(e.call("PUT", p("/curriculum-versions/%d/graduation-rules", w.v2020), e.reg, rules).Status == 200, "kural kaydı")

	// 2021 girişli (2020 müfredatı: A,B,C,D zorunlu) öğrenci; D eksik, F ve G geçilmiş.
	s := e.newStudent(t, "f7", w.prog, 2021, nil)
	w.f7 = s
	for _, c := range []string{"A", "B", "C", "F", "G"} {
		e.pass(t, s.sid, w.course[c], w.term0, "BB", 3)
	}
	r := e.call("GET", p("/students/%d/graduation-audit", s.sid), s.token, nil)
	t.must(r.Status == 200 && !r.boolean("report.can_graduate"), "D eksikken mezun görünüyor: %s", r.Raw)
	t.must(len(r.arr("report.required_missing")) == 1 && r.arr("report.required_missing")[0] == e.code("D"), "eksik zorunlu ders: %s", r.Raw)
	rem := r.arr("remaining_required_courses")
	t.must(len(rem) == 1 && rem[0].(M)["code"] == e.code("D"), "'kalan dersler' raporu: %s", r.Raw)
	t.must(len(r.arr("report.missing")) >= 1, "özet satırı yok")

	e.pass(t, s.sid, w.course["D"], w.term0, "AA", 4)
	r = e.call("GET", p("/students/%d/graduation-audit", s.sid), s.token, nil)
	t.must(r.boolean("report.can_graduate"), "tüm şartlar sağlandı, mezun olabilmeli: %s", r.Raw)
	t.must(e.call("GET", p("/students/%d/graduation-audit", s.sid), w.gs[0].token, nil).Status == 403, "başka öğrenci mezuniyet raporu gördü")
}

func chkF8(t *T, e *env) {
	w := e.world()
	succ := e.call("GET", p("/reports/department-success?term_id=%d", w.term), e.rep, nil)
	t.must(succ.Status == 200, "başarı raporu: %d %s", succ.Status, succ.Raw)
	var found bool
	for _, x := range succ.arr("items") {
		m := x.(M)
		if int64(m["department_id"].(float64)) == w.dept {
			found = true
			t.must(m["results"].(float64) == 4 && m["passed"].(float64) == 3 && m["pass_rate"].(float64) == 75,
				"bölüm başarısı (4 sonuç, 3 geçen, %%75 beklenir): %v", m)
		}
	}
	t.must(found, "bölüm başarı raporunda yok")

	occ := e.call("GET", p("/reports/occupancy?term_id=%d", w.term), e.reg, nil)
	t.must(occ.Status == 200, "doluluk: %d", occ.Status)
	found = false
	for _, x := range occ.arr("items") {
		m := x.(M)
		if int64(m["department_id"].(float64)) == w.dept {
			found = true
			t.must(m["sections"].(float64) == 5 && m["capacity"].(float64) > 0, "doluluk satırı: %v", m)
		}
	}
	t.must(found, "bölüm doluluk raporunda yok")

	dist := e.call("GET", p("/reports/gano-distribution?department_id=%d", w.dept), e.sec, nil)
	t.must(dist.Status == 200 && len(dist.arr("buckets")) == 6, "GANO dağılımı: %s", dist.Raw)
	var total float64
	for _, b := range dist.arr("buckets") {
		total += b.(M)["count"].(float64)
	}
	t.must(total == dist.num("total_students") && total >= 4, "aralık toplamı %v, toplam öğrenci %v", total, dist.num("total_students"))
	t.must(e.call("GET", p("/reports/occupancy?term_id=%d", w.term), e.stu, nil).Status == 403, "öğrenci rapor gördü")
	t.must(e.call("GET", "/reports/occupancy", e.rep, nil).Status == 400, "term_id olmadan rapor üretildi")
}

// chkBonusRisk: danışman yalnızca kendi danışanlarından riskli olanı görür; sağlıklı öğrenci listelenmez.
func chkBonusRisk(t *T, e *env) {
	w := e.world()
	mk := func(code string) int64 {
		r := e.call("POST", "/terms", e.reg, M{"code": code + e.sfx, "name": "Risk dönemi " + code, "registration_open": false})
		t.must(r.Status == 201, "dönem %s: %s", code, r.Raw)
		return r.id("id")
	}
	prev, last := mk("VRP"), mk("VRL") // kimlik sırası = zaman sırası

	risky := e.newStudent(t, "riskli", w.prog, 2022, &w.advisor)
	e.pass(t, risky.sid, w.course["A"], prev, "BB", 3)
	e.pass(t, risky.sid, w.course["B"], prev, "BB", 3)
	e.pass(t, risky.sid, w.course["C"], last, "FF", 0)
	e.pass(t, risky.sid, w.course["D"], last, "FF", 0)
	fine := e.newStudent(t, "saglam", w.prog, 2022, &w.advisor)
	e.pass(t, fine.sid, w.course["A"], prev, "BB", 3)
	e.pass(t, fine.sid, w.course["A"], last, "BA", 3.5)
	other := e.newStudent(t, "baskasi", w.prog, 2022, nil) // danışanı değil
	e.pass(t, other.sid, w.course["A"], last, "FF", 0)

	r := e.call("GET", "/advisor/at-risk", w.advisor.token, nil)
	t.must(r.Status == 200, "risk listesi: %d %s", r.Status, r.Raw)
	var found bool
	for _, it := range r.arr("items") {
		m, _ := it.(map[string]any)
		sid, _ := m["student_id"].(float64)
		switch int64(sid) {
		case fine.sid:
			t.fail("sağlıklı öğrenci riskli listelendi")
		case other.sid:
			t.fail("danışanı olmayan öğrenci görüldü (yetki sızıntısı)")
		case risky.sid:
			found = true
			reasons := fmt.Sprint(m["reasons"])
			t.must(reasons == "[low_gano gpa_drop failed_courses]", "uyarı kodları: %s", reasons)
		}
	}
	t.must(found, "riskli öğrenci listelenmedi: %s", r.Raw)
	t.must(e.call("GET", "/advisor/at-risk", risky.token, nil).Status == 403, "öğrenci riskli listesine erişti")
	t.must(e.call("GET", "/advisor/at-risk", "", nil).Status == 401, "anonim erişim")
}
