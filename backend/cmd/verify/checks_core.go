package main

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/batuhan-daylan/uninet/internal/simulator"
)

func (e *env) newCourse(t *T, w *world, label string, ects int) int64 {
	r := e.call("POST", "/courses", e.reg, M{"code": "V" + label + e.sfx, "name": "Ders " + label, "ects": ects, "department_id": w.dept})
	t.must(r.Status == 201, "ders %s: %d %s", label, r.Status, r.Raw)
	return r.id("id")
}

func (e *env) newSection(t *T, w *world, courseID int64, code string, capacity int, instructor *user) int64 {
	body := M{"term_id": w.term, "course_id": courseID, "code": code, "capacity": capacity}
	if instructor != nil {
		body["instructor_id"] = instructor.id
	}
	r := e.call("POST", "/sections", e.reg, body)
	t.must(r.Status == 201, "şube: %d %s", r.Status, r.Raw)
	return r.id("id")
}

func (e *env) count(t *T, sql string, args ...any) int {
	var n int
	t.must(e.pool.QueryRow(context.Background(), sql, args...).Scan(&n) == nil, "sorgu: %s", sql)
	return n
}

func chkT1(t *T, e *env) {
	w := e.world()
	const n, capacity = 60, 10
	sec := e.newSection(t, w, e.newCourse(t, w, "T1", 3), "01", capacity, nil)
	sts := e.newStudents(t, "t1-", n, w.prog, 2025)

	// 60 öğrenci AYNI ANDA, kontenjan 10.
	res := make([]R, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range sts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res[i] = e.enroll(sts[i].token, sec, p("t1-%s-%d", e.sfx, i))
		}()
	}
	close(start)
	wg.Wait()

	enrolled, waitlisted := 0, 0
	positions := map[int]bool{}
	for i, r := range res {
		t.must(r.Status == 201, "istek %d: %d %s", i, r.Status, r.Raw)
		switch r.str("status") {
		case "enrolled":
			enrolled++
		case "waitlisted":
			waitlisted++
			positions[int(r.num("waitlist_position"))] = true
		}
	}
	t.must(enrolled == capacity && waitlisted == n-capacity, "kayıt %d / bekleme %d, beklenen %d / %d", enrolled, waitlisted, capacity, n-capacity)
	t.must(len(positions) == n-capacity, "bekleme sırası tekrarsız değil (%d farklı sıra)", len(positions))
	sc := e.call("GET", p("/sections/%d", sec), sts[0].token, nil)
	t.must(sc.num("enrolled_count") == capacity && sc.num("waitlist_size") == n-capacity, "şube sayaçları: %s", sc.Raw)

	// Adil sıra: koltuk alan her kayıt, bekleyen her kayıttan önce gelmiş olmalı.
	bad := e.count(t, `SELECT count(*) FROM enrollments w JOIN enrollments en ON en.section_id = w.section_id
	                    WHERE w.section_id = $1 AND w.status = 'waitlisted' AND en.status = 'enrolled' AND en.id > w.id`, sec)
	t.must(bad == 0, "sıra ihlali: %d çift", bad)

	// 3 koltuk sahibi aynı anda bırakır: bekleme listesinin ilk 3 kişisi OTOMATİK atanmalı.
	var enrolledRows []struct{ enr, stu int64 }
	rows, err := e.pool.Query(context.Background(), `SELECT id, student_id FROM enrollments WHERE section_id=$1 AND status='enrolled' ORDER BY id LIMIT 3`, sec)
	t.must(err == nil, "koltuk sahipleri")
	for rows.Next() {
		var x struct{ enr, stu int64 }
		t.must(rows.Scan(&x.enr, &x.stu) == nil, "tarama")
		enrolledRows = append(enrolledRows, x)
	}
	rows.Close()
	tokByStudent := map[int64]string{}
	for _, s := range sts {
		tokByStudent[s.sid] = s.token
	}
	for _, x := range enrolledRows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.call("DELETE", p("/enrollments/%d", x.enr), tokByStudent[x.stu], nil)
			if r.Status != 200 {
				t.fail("bırakma: %d %s", r.Status, r.Raw)
			}
		}()
	}
	wg.Wait()
	t.must(len(t.errs) == 0, "eşzamanlı bırakma başarısız")
	t.must(e.count(t, `SELECT enrolled_count FROM sections WHERE id=$1`, sec) == capacity, "koltuk sayısı bırakmalardan sonra %d olmalıydı", capacity)
	// İlk 3 bekleyen (id sırasıyla) artık 'enrolled' ve terfi tarihi dolu olmalı.
	promoted := e.count(t, `SELECT count(*) FROM (SELECT id FROM enrollments WHERE section_id=$1 AND promoted_at IS NOT NULL) x`, sec)
	t.must(promoted == 3, "otomatik atanan %d, beklenen 3", promoted)
	skipped := e.count(t, `SELECT count(*) FROM enrollments w WHERE w.section_id=$1 AND w.status='waitlisted'
	                       AND EXISTS (SELECT 1 FROM enrollments p WHERE p.section_id=w.section_id AND p.promoted_at IS NOT NULL AND p.id > w.id)`, sec)
	t.must(skipped == 0, "sıra atlandı: %d bekleyen, kendinden sonra gelen terfi ettirildi", skipped)

	// "Kontenjan aşımı yok" doğrulaması (veritabanı).
	v, err := simulator.CheckQuota(context.Background(), e.pool)
	t.must(err == nil && len(v) == 0, "veritabanı tutarlılık ihlali: %v %v", v, err)
	t.note("60 eşzamanlı istek → tam %d kayıt, %d bekleme; sıra adil; otomatik atama sırayla", capacity, n-capacity)
}

func chkT2(t *T, e *env) {
	w := e.world()
	P1, P2, P3 := e.newCourse(t, w, "P1", 5), e.newCourse(t, w, "P2", 5), e.newCourse(t, w, "P3", 5)
	Q, X, BIG := e.newCourse(t, w, "Q", 5), e.newCourse(t, w, "X", 5), e.newCourse(t, w, "BIG", 30)
	// Q için ön koşul: P1 ve (P2 veya P3).
	expr := M{"op": "and", "args": []M{{"course_id": P1}, {"op": "or", "args": []M{{"course_id": P2}, {"course_id": P3}}}}}
	t.must(e.call("PUT", p("/courses/%d/prerequisites", Q), e.reg, M{"expr": expr}).Status == 200, "ön koşul kaydı")

	// DAG: döngü ve geçersiz ifade reddedilir.
	cyc := e.call("PUT", p("/courses/%d/prerequisites", P1), e.reg, M{"expr": M{"course_id": Q}})
	t.must(cyc.Status == 422 && cyc.str("error.code") == "prerequisite_cycle", "döngü reddedilmedi: %d %s", cyc.Status, cyc.Raw)
	t.must(e.call("PUT", p("/courses/%d/prerequisites", P1), e.reg, M{"expr": M{"course_id": P1}}).Status == 422, "öz-döngü reddedilmedi")
	t.must(e.call("PUT", p("/courses/%d/prerequisites", P1), e.reg, M{"expr": M{"op": "xor", "args": []M{{"course_id": P2}}}}).Status == 400, "geçersiz ifade kabul edildi")
	t.must(e.call("PUT", p("/courses/%d/prerequisites", P1), e.stu, M{"expr": M{"course_id": P2}}).Status == 403, "öğrenci ön koşul tanımladı")

	Qs := e.newSection(t, w, Q, "01", 30, nil)
	Qs2 := e.newSection(t, w, Q, "02", 30, nil) // aynı dersin ikinci şubesi
	Xs := e.newSection(t, w, X, "01", 30, nil)
	Bs := e.newSection(t, w, BIG, "01", 30, nil)
	for _, m := range []struct {
		sec       int64
		day, a, b int
	}{{Qs, 2, 540, 660}, {Xs, 2, 600, 720}} { // Qs ve Xs Salı günü çakışır
		t.must(e.call("POST", p("/sections/%d/meetings", m.sec), e.reg, M{"day": m.day, "start_min": m.a, "end_min": m.b}).Status == 201, "ders saati")
	}

	s := e.newStudent(t, "t2", w.prog, 2025, nil)
	// TEK istekte: ön koşul + tekrar + çakışma + AKTS ihlalleri BİRLİKTE listelenmeli (ilk hatada durmamalı).
	v := e.call("POST", "/me/enrollments/validate", s.token, M{"section_ids": []int64{Qs, Qs2, Xs, Bs}})
	t.must(v.Status == 200 && !v.boolean("valid"), "doğrulama: %d %s", v.Status, v.Raw)
	// Aynı dersin iki şubesi için ön koşul ihlali şube başına bir kez raporlanır; türleri benzersiz karşılaştırırız.
	got := uniq(v.listField("violations", "code"))
	want := []string{"DUPLICATE_COURSE", "ECTS_LIMIT_EXCEEDED", "PREREQ_NOT_MET", "TIME_CONFLICT"}
	t.must(fmt.Sprint(got) == fmt.Sprint(want), "ihlaller %v, beklenen hepsi birlikte %v", got, want)
	t.must(v.num("total_ects") == 45 && v.num("max_ects") == 30, "AKTS %v/%v, beklenen 45/30", v.num("total_ects"), v.num("max_ects"))
	var msg string
	for _, x := range v.arr("violations") {
		if m := x.(M); m["code"] == "PREREQ_NOT_MET" {
			msg = m["message"].(string)
		}
	}
	t.must(containsAll(msg, "veya", "ve"), "ön koşul mesajı ifadeyi açıklamıyor: %q", msg)

	// Gerçek kayıt da aynı kurallarla reddedilir (422 + liste); hiçbir şey yazılmaz.
	en := e.enroll(s.token, Qs, p("t2-%s", e.sfx))
	t.must(en.Status == 422 && en.str("error.code") == "rule_violation" && containsStr(en.listField("error.violations", "code"), "PREREQ_NOT_MET"), "kayıt reddedilmedi: %d %s", en.Status, en.Raw)
	t.must(e.count(t, `SELECT count(*) FROM enrollments WHERE student_id=$1`, s.sid) == 0, "reddedilen kayıt veritabanına yazıldı")

	// İfade: P2+P3 var ama P1 yok → VE yüzünden sağlanmaz; P1+P2 ile sağlanır (VEYA).
	e.pass(t, s.sid, P2, w.term0, "BB", 3)
	e.pass(t, s.sid, P3, w.term0, "BB", 3)
	v = e.call("POST", "/me/enrollments/validate", s.token, M{"section_ids": []int64{Qs}})
	t.must(!v.boolean("valid") && containsStr(v.listField("violations", "code"), "PREREQ_NOT_MET"), "P1 olmadan ön koşul sağlandı: %s", v.Raw)
	e.pass(t, s.sid, P1, w.term0, "BB", 3)
	v = e.call("POST", "/me/enrollments/validate", s.token, M{"section_ids": []int64{Qs}})
	t.must(v.boolean("valid"), "ön koşul sağlandığı hâlde reddedildi: %s", v.Raw)
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !contains(s, x) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func chkT3(t *T, e *env) {
	w := e.world()
	s := e.newStudent(t, "t3", w.prog, 2021, nil)

	cur := e.call("GET", p("/students/%d/curriculum", s.sid), s.token, nil)
	t.must(cur.Status == 200 && cur.num("effective_year") == 2020 && len(cur.arr("courses")) == 4, "2021 girişli 2020 müfredatında olmalı: %s", cur.Raw)
	// Sonradan yeni müfredat açılır: eski öğrenci ETKİLENMEMELİ, yeni girişli yenisine bağlanır.
	nv := e.call("POST", p("/programs/%d/curriculum-versions", w.prog), e.reg, M{"effective_year": 2027, "courses": []M{{"course_id": w.course["G"], "semester": 1, "kind": "required"}}})
	t.must(nv.Status == 201, "2027 sürümü: %s", nv.Raw)
	cur = e.call("GET", p("/students/%d/curriculum", s.sid), s.token, nil)
	t.must(cur.num("effective_year") == 2020 && len(cur.arr("courses")) == 4, "yeni müfredat eski öğrenciyi etkiledi: %s", cur.Raw)
	n := e.newStudent(t, "t3n", w.prog, 2028, nil)
	cur = e.call("GET", p("/students/%d/curriculum", n.sid), n.token, nil)
	t.must(cur.num("effective_year") == 2027, "2028 girişli 2027 müfredatına bağlanmalı: %s", cur.Raw)

	// Zaman yolculuğu: 2021 girişli öğrencinin 2024 itibarıyla alması gereken dersler.
	rq := e.call("GET", p("/students/%d/required-courses?as_of=2024", s.sid), s.token, nil)
	t.must(rq.Status == 200 && rq.num("study_year") == 4 && rq.num("curriculum.effective_year") == 2020, "zaman yolculuğu: %s", rq.Raw)
	mt := rq.listField("must_take", "code")
	t.must(fmt.Sprint(mt) == fmt.Sprint([]string{e.code("A"), e.code("B"), e.code("C"), e.code("D")}), "alması gereken dersler %v (2020 müfredatının A,B,C,D'si bekleniyordu; 2024 müfredatından E,F gelmemeli)", mt)

	// Denklik: "C yerine E sayılır". Öğrenci E'yi geçti → C karşılanmış sayılır.
	t.must(e.call("POST", "/course-equivalences", e.reg, M{"course_id": w.course["C"], "substitute_course_id": w.course["E"]}).Status == 201, "denklik kaydı")
	t.must(e.call("POST", "/course-equivalences", e.reg, M{"course_id": w.course["C"], "substitute_course_id": w.course["E"]}).Status == 409, "aynı denklik tekrar kabul edildi")
	t.must(e.call("POST", "/course-equivalences", e.reg, M{"course_id": w.course["C"], "substitute_course_id": w.course["C"]}).Status == 400, "öz-denklik kabul edildi")
	e.pass(t, s.sid, w.course["E"], w.term0, "BB", 3)
	rq = e.call("GET", p("/students/%d/required-courses?as_of=2024", s.sid), s.token, nil)
	var cStatus, via string
	for _, x := range rq.arr("courses") {
		if m := x.(M); m["code"] == e.code("C") {
			cStatus, via = m["status"].(string), fmt.Sprint(m["via_course"])
		}
	}
	t.must(cStatus == "equivalent" && via == e.code("E"), "C denklikle karşılanmalıydı: durum=%s via=%s", cStatus, via)

	// Muafiyet yalnızca öğrenci işlerince verilir; ders muaf görünür.
	t.must(e.call("POST", p("/students/%d/exemptions", s.sid), s.token, M{"course_id": w.course["D"], "term_id": w.term0}).Status == 403, "öğrenci kendine muafiyet verdi")
	t.must(e.call("POST", p("/students/%d/exemptions", s.sid), e.reg, M{"course_id": w.course["D"], "term_id": w.term0}).Status == 201, "muafiyet verilemedi")
	rq = e.call("GET", p("/students/%d/required-courses?as_of=2024", s.sid), s.token, nil)
	t.must(fmt.Sprint(rq.listField("must_take", "code")) == fmt.Sprint([]string{e.code("A"), e.code("B")}), "denklik+muafiyetten sonra yalnızca A,B kalmalı: %v", rq.listField("must_take", "code"))
	// Denklik ön koşulu da karşılar; muafiyet GANO'ya girmez (öğrencinin tek notlu dersi E=3.0).
	res := e.call("GET", p("/students/%d/results", s.sid), s.token, nil)
	t.must(math.Abs(res.num("gano")-3.0) < 0.001, "GANO %v, beklenen 3.0 (muaf ders sayılmaz)", res.num("gano"))
}

func chkT4(t *T, e *env) {
	w := e.world()
	rel := e.newCourse(t, w, "R", 4)
	sec := e.newSection(t, w, rel, "01", 20, &w.instrA)
	var enr []int64
	var sts []student
	for i := 0; i < 8; i++ {
		s := e.newStudent(t, p("t4-%d", i), w.prog, 2024, nil)
		r := e.enroll(s.token, sec, p("t4-%s-%d", e.sfx, i))
		t.must(r.Status == 201, "kayıt: %s", r.Raw)
		sts = append(sts, s)
		enr = append(enr, r.id("enrollment_id"))
	}
	ia := w.instrA.token
	t.must(e.call("PUT", p("/sections/%d/grade-components", sec), ia, M{"method": "relative", "components": []M{{"name": "midterm", "weight": 50}, {"name": "final", "weight": 50}}}).Status == 200, "bileşenler")
	totals := []float64{95, 88, 80, 72, 65, 58, 50, 35}
	var entries []M
	for i, tot := range totals {
		if i == 3 { // mazeret sınavı: vize girilmemiş, mazeret 80; final 64 → toplam 72
			entries = append(entries, M{"enrollment_id": enr[i], "component": "midterm", "kind": "excuse", "score": 80}, M{"enrollment_id": enr[i], "component": "final", "kind": "regular", "score": 64})
			continue
		}
		entries = append(entries, M{"enrollment_id": enr[i], "component": "midterm", "kind": "regular", "score": tot}, M{"enrollment_id": enr[i], "component": "final", "kind": "regular", "score": tot})
	}
	t.must(e.call("PUT", p("/sections/%d/scores", sec), ia, M{"entries": entries}).Status == 200, "puan girişi")
	pub := e.call("POST", p("/sections/%d/grades/publish", sec), ia, nil)
	t.must(pub.Status == 200 && pub.str("method") == "relative", "ilan: %d %s", pub.Status, pub.Raw)

	type row struct {
		total  float64
		letter string
	}
	byStudent := map[int64]row{}
	for _, x := range pub.arr("results") {
		m := x.(M)
		byStudent[int64(m["student_id"].(float64))] = row{m["total"].(float64), m["letter"].(string)}
	}
	t.must(byStudent[sts[3].sid].total == 72, "mazeret sınavı toplamda geçerli sayılmadı: %v", byStudent[sts[3].sid])
	rank := map[string]int{"AA": 0, "BA": 1, "BB": 2, "CB": 3, "CC": 4, "DC": 5, "DD": 6, "FF": 7}
	for i := range totals {
		for j := range totals {
			a, b := byStudent[sts[i].sid], byStudent[sts[j].sid]
			t.must(!(a.total > b.total && rank[a.letter] > rank[b.letter]), "bağıl değerlendirme tekdüze değil: %v → %s, %v → %s", a.total, a.letter, b.total, b.letter)
		}
	}
	t.must(byStudent[sts[0].sid].letter == "AA" || byStudent[sts[0].sid].letter == "BA", "en yüksek puan %s aldı", byStudent[sts[0].sid].letter)
	t.must(byStudent[sts[7].sid].letter == "FF", "taban altı (35) bağıl yöntemde FF olmalı: %s", byStudent[sts[7].sid].letter)

	// Arka plan işi: GANO önbelleği kaynak veriden hesaplanan değere yakınsar.
	deadline := time.Now().Add(20 * time.Second)
	var last R
	for time.Now().Before(deadline) {
		last = e.call("GET", p("/students/%d/results", sts[0].sid), sts[0].token, nil)
		if last.num("gano_cached") > 0 && math.Abs(last.num("gano")-last.num("gano_cached")) < 0.001 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.must(last.num("gano_cached") > 0 && math.Abs(last.num("gano")-last.num("gano_cached")) < 0.001, "GANO işçisi önbelleği güncellemedi: %s", last.Raw)

	// Tekrar alınan ders politikası: aynı ders iki kez; ikincisi daha kötü.
	s := e.newStudent(t, "t4r", w.prog, 2024, nil)
	e.pass(t, s.sid, w.course["A"], w.term, "BB", 3.0)  // önce BB
	e.pass(t, s.sid, w.course["A"], w.term0, "DC", 1.5) // sonra DC (daha kötü)
	g := func() (live, cached float64) {
		r := e.call("GET", p("/students/%d/results", s.sid), s.token, nil)
		return r.num("gano"), r.num("gano_cached")
	}
	live, _ := g()
	t.must(math.Abs(live-1.5) < 0.001, "'son not' politikasında GANO %v, beklenen 1.5", live)
	pol := e.call("PUT", p("/programs/%d/retake-policy", w.prog), e.reg, M{"policy": "best"})
	t.must(pol.Status == 200 && pol.num("recalculating_students") >= 1, "politika değişimi: %s", pol.Raw)
	deadline = time.Now().Add(30 * time.Second)
	var cached float64
	for time.Now().Before(deadline) {
		live, cached = g()
		if math.Abs(live-3.0) < 0.001 && math.Abs(cached-3.0) < 0.001 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.must(math.Abs(live-3.0) < 0.001 && math.Abs(cached-3.0) < 0.001, "'en yüksek not' politikasında GANO %v (önbellek %v), beklenen 3.0", live, cached)
	t.must(e.call("PUT", p("/programs/%d/retake-policy", w.prog), e.sec, M{"policy": "last"}).Status == 403, "bölüm sekreteri politika değiştirdi")
	t.must(e.call("PUT", p("/programs/%d/retake-policy", w.prog), e.reg, M{"policy": "last"}).Status == 200, "politika geri alınamadı")
}

func chkT5(t *T, e *env) {
	w := e.world()
	solve := func(apply bool) R {
		return e.call("POST", p("/terms/%d/schedule/solve", w.term), e.reg, M{"apply": apply, "time_limit_seconds": 20})
	}
	r := solve(false)
	t.must(r.Status == 200 && r.str("status") == "solved" && !r.boolean("applied"), "çözüm: %d %s", r.Status, r.Raw)
	t.note("%v şube, %v derslik, %v öğretim üyesi çözüldü (%v ms)", r.num("sections"), r.num("rooms"), r.num("instructors"), r.num("elapsed_ms"))

	// Çözümsüzlük: öğretim üyesi A bütün hafta müsait değil → hangi kısıtın çatıştığı raporlanır.
	var slots []M
	for d := 1; d <= 5; d++ {
		for s := 0; s < 8; s++ {
			slots = append(slots, M{"day": d, "slot": s})
		}
	}
	t.must(e.call("PUT", p("/instructors/%d/unavailability", w.instrA.id), e.reg, M{"slots": slots}).Status == 200, "müsaitlik")
	inf := solve(true)
	t.must(inf.Status == 200 && inf.str("status") == "infeasible" && !inf.boolean("applied"), "çözümsüz bekleniyordu: %d %s", inf.Status, inf.Raw)
	kinds := inf.listField("conflicts", "kind")
	t.must(containsStr(kinds, "INSTRUCTOR_UNAVAILABLE"), "çatışan kısıt adlandırılmadı: %v", kinds)
	for _, x := range inf.arr("conflicts") {
		t.must(x.(M)["message"].(string) != "", "açıklamasız çatışma")
	}
	// Müsaitlik geri verilince tekrar çözülür.
	t.must(e.call("PUT", p("/instructors/%d/unavailability", w.instrA.id), e.reg, M{"slots": []M{}}).Status == 200, "müsaitlik geri yüklenemedi")
	t.must(solve(false).str("status") == "solved", "müsaitlik geri verildiği hâlde çözülemedi")
	t.must(e.call("POST", p("/terms/%d/schedule/solve", w.term), e.reg, M{"days": 20, "slots_per_day": 8}).Status == 400, "geçersiz ızgara kabul edildi")
	t.must(e.call("POST", p("/terms/%d/schedule/solve", w.term), e.stu, nil).Status == 403, "öğrenci çözücüyü çalıştırdı")
	t.note("60 ders / 12 derslik / 20 öğretim üyesi ölçeği birim testlerinde ms düzeyinde çözülüyor (internal/scheduler)")
}

func chkT6(t *T, e *env) {
	w := e.world()
	s := w.f7 // F-7'de kuruldu: A,B,C,D,F,G geçmiş; kurallar P1[E,F] ve P2[F,G]
	audit := func() R { return e.call("GET", p("/students/%d/graduation-audit", s.sid), s.token, nil) }
	r := audit()
	t.must(r.boolean("report.can_graduate"), "başlangıç durumu mezun olabilmeli: %s", r.Raw)
	pools := r.arr("report.pools")
	t.must(len(pools) == 2, "havuz sayısı %d", len(pools))
	// F iki havuzda da geçerli ama yalnızca birine sayılır: en uygun atama F→P1 ve G→P2 (ya da tersi) bulunmalı.
	assigned := map[string]bool{}
	for _, x := range pools {
		for _, c := range x.(M)["assigned"].([]any) {
			assigned[c.(string)] = true
		}
	}
	t.must(assigned[e.code("F")] && assigned[e.code("G")] && len(assigned) == 2, "en uygun atama bulunamadı: %v", assigned)

	// Kurallar VERİDİR: kod değişmeden yalnızca JSON güncellenerek sonuç değişir.
	setRules := func(rules M) R {
		return e.call("PUT", p("/curriculum-versions/%d/graduation-rules", w.v2020), e.reg, rules)
	}
	t.must(setRules(M{"min_gano": 0, "min_total_ects": 0, "pools": []M{
		{"name": "P1", "courses": []string{e.code("E"), e.code("F")}, "min_courses": 2}}}).Status == 200, "sıkı kural")
	r = audit()
	t.must(!r.boolean("report.can_graduate") && len(r.arr("report.missing")) >= 1, "sıkı kuralda mezun görünüyor: %s", r.Raw)
	// Staj dersi: E staj olarak ayrılırsa havuzlara sayılmaz; öğrenci E'yi geçmediği için staj eksik.
	t.must(setRules(M{"min_gano": 0, "min_total_ects": 0, "internship": M{"course": e.code("E")}, "pools": []M{
		{"name": "P1", "courses": []string{e.code("F")}, "min_courses": 1}}}).Status == 200, "staj kuralı")
	r = audit()
	t.must(!r.boolean("report.internship.ok") && !r.boolean("report.can_graduate"), "staj eksikken mezun görünüyor: %s", r.Raw)
	e.pass(t, s.sid, w.course["E"], w.term0, "CC", 2)
	r = audit()
	t.must(r.boolean("report.internship.ok") && r.boolean("report.can_graduate"), "staj tamamlandı, mezun olabilmeli: %s", r.Raw)
	// GANO ve toplam AKTS eşikleri de kurallardan gelir.
	t.must(setRules(M{"min_gano": 3.9, "min_total_ects": 0, "pools": []M{}}).Status == 200, "GANO kuralı")
	r = audit()
	t.must(!r.boolean("report.gano_ok") && !r.boolean("report.can_graduate"), "GANO eşiği uygulanmadı: %s", r.Raw)
	// Doğrulama: olmayan ders 422, yetkisiz yazma 403, kural değişiklikleri denetlenir.
	t.must(setRules(M{"pools": []M{{"name": "X", "courses": []string{"YOKTUR"}, "min_courses": 1}}}).Status == 422, "olmayan ders kabul edildi")
	t.must(e.call("PUT", p("/curriculum-versions/%d/graduation-rules", w.v2020), e.stu, M{"min_gano": 0}).Status == 403, "öğrenci kural yazdı")
	t.must(e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'graduation_rules.set' AND entity_id = $1`, fmt.Sprint(w.v2020)) >= 4, "kural değişiklikleri denetim kaydına yazılmadı")
}

func chkT7(t *T, e *env) {
	w := e.world()
	secC := w.sections["C"]
	// Öğretim üyesi yalnızca KENDİ şubesinin notlarını görür.
	t.must(e.call("GET", p("/sections/%d/results", secC), w.instrA.token, nil).Status == 200, "şubenin öğretim üyesi sonuçları göremedi")
	t.must(e.call("GET", p("/sections/%d/results", secC), w.instrB.token, nil).Status == 403, "başka öğretim üyesi şubenin notlarını gördü")
	// Danışman yalnızca KENDİ danışanlarını görür.
	mine := e.newStudent(t, "t7a", w.prog, 2023, &w.advisor)
	t.must(e.call("GET", p("/students/%d/results", mine.sid), w.advisor.token, nil).Status == 200, "danışman danışanını göremedi")
	t.must(e.call("GET", p("/students/%d/results", w.gs[0].sid), w.advisor.token, nil).Status == 403, "danışman danışanı olmayan öğrenciyi gördü")
	t.must(e.call("GET", p("/students/%d/results", mine.sid), e.adv, nil).Status == 403, "başka danışman öğrenciyi gördü")

	// Not ilanından sonra giriş kilitli.
	lock := e.call("PUT", p("/sections/%d/scores", secC), w.instrA.token, M{"entries": []M{{"enrollment_id": 1, "component": "midterm", "kind": "regular", "score": 1}}})
	t.must(lock.Status == 409 && lock.str("error.code") == "grades_published", "ilandan sonra giriş kilitlenmedi: %d %s", lock.Status, lock.Raw)

	// Geçmişe dönük değişiklik ÇİFT ONAY ister: talep eden kendi talebini onaylayamaz.
	res := w.gradeResult[w.gs[3].sid] // FF alan öğrenci
	cr := e.call("POST", p("/course-results/%d/corrections", res), w.instrA.token, M{"new_letter": "DD", "reason": "Maddi hata"})
	t.must(cr.Status == 201, "düzeltme talebi: %d %s", cr.Status, cr.Raw)
	cid := cr.id("id")
	t.must(e.call("POST", p("/course-results/%d/corrections", res), w.instrB.token, M{"new_letter": "DD", "reason": "x"}).Status == 403, "başka öğretim üyesi talep açtı")
	t.must(e.call("POST", p("/grade-corrections/%d/decision", cid), w.instrA.token, M{"approve": true}).Status == 403, "öğretim üyesi kendi talebini onayladı")
	t.must(e.call("POST", p("/grade-corrections/%d/decision", cid), w.gs[3].token, M{"approve": true}).Status == 403, "öğrenci kendi notunu onayladı")
	t.must(e.count(t, `SELECT count(*) FROM course_results WHERE id=$1 AND letter='FF'`, res) == 1, "onaysız not değişti")
	t.must(e.call("POST", p("/grade-corrections/%d/decision", cid), e.reg, M{"approve": true}).Status == 200, "ikinci kişi onayı")
	t.must(e.count(t, `SELECT count(*) FROM course_results WHERE id=$1 AND letter='DD'`, res) == 1, "onaydan sonra not değişmedi")
	t.must(e.count(t, `SELECT count(*) FROM grade_history WHERE result_id=$1 AND old_letter='FF' AND new_letter='DD' AND requested_by<>approved_by`, res) == 1, "tam geçmiş (eski/yeni/talep eden/onaylayan) yazılmadı")

	// Değiştirilemez denetim kaydı: UPDATE/DELETE/TRUNCATE veritabanı düzeyinde reddedilir (not geçmişi dahil).
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE audit_log SET action = 'x'`, `DELETE FROM audit_log`, `TRUNCATE audit_log`,
		`UPDATE grade_history SET new_letter = 'AA'`, `DELETE FROM grade_history`, `UPDATE documents SET snapshot = 'x'`,
	} {
		_, err := e.pool.Exec(ctx, q)
		t.soft(err != nil, "değiştirilemez olması gereken tablo değiştirilebildi: %s", q)
	}

	// Her işlem denetim kaydı bırakır: kim, ne zaman, neyi, eski/yeni değer.
	before := e.count(t, `SELECT count(*) FROM audit_log`)
	f := e.call("POST", "/faculties", e.reg, M{"code": "AUD" + e.sfx, "name": "Denetim"})
	t.must(f.Status == 201, "fakülte")
	t.must(e.count(t, `SELECT count(*) FROM audit_log`) > before, "yazma işlemi denetim kaydı bırakmadı")
	t.must(e.count(t, `SELECT count(*) FROM audit_log WHERE action='faculty.create' AND entity_id=$1 AND actor_id IS NOT NULL AND new_value IS NOT NULL AND at IS NOT NULL`, fmt.Sprint(int64(f.num("id")))) == 1, "denetim kaydında kim/neyi/yeni değer eksik")
	t.must(e.count(t, `SELECT count(*) FROM audit_log WHERE action='grade_correction.approved' AND entity_id=$1 AND old_value IS NOT NULL AND new_value IS NOT NULL`, fmt.Sprint(res)) == 1, "düzeltme denetim kaydında eski/yeni değer yok")
	t.must(e.count(t, `SELECT count(*) FROM audit_log WHERE action='section.grades_published' AND entity_id=$1`, fmt.Sprint(secC)) == 1, "not ilanı denetim kaydı yok")

	// İmzalı belge: HMAC'li kod doğrulanır; kod başka bir belge kimliğine uyarlanırsa reddedilir.
	d := e.call("POST", p("/students/%d/transcripts", w.gs[1].sid), w.gs[1].token, nil)
	t.must(d.Status == 201, "belge")
	t.must(e.call("GET", "/verify/"+d.str("code"), "", nil).Status == 200, "geçerli belge doğrulanamadı")
	sig := d.str("code")[len(fmt.Sprint(d.id("id"))):]
	forged := fmt.Sprint(d.id("id")+1) + sig
	t.must(e.call("GET", "/verify/"+forged, "", nil).Status == 404, "başka belge kimliğine uyarlanmış imza kabul edildi")
}

// uniq sıralı bir dizgi listesindeki tekrarları kaldırır.
func uniq(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}
