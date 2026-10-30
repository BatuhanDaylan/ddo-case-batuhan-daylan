package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"time"
)

func allChecks() []check {
	return []check{
		{"HEALTH", "GET /health (veritabanı dahil), /metrics, JSON 404", chkHealth},
		{"AUTH", "yanlış parola, token'sız/sahte erişim, refresh rotasyonu + yeniden kullanım tespiti", chkAuth},
		{"F-1", "akademik yapı: fakülte→bölüm→program→ders→müfredat sürümleri, sayfalama", chkF1},
		{"AUTHZ", "yetki ihlalleri sunucuda reddedilir (401/403), girdi doğrulaması (400)", chkAuthz},
		{"F-2", "dönem ve şube yönetimi: kontenjan, derslik, saat, öğretim üyesi", chkF2},
		{"F-3", "ders kaydı: bekleme listesi, danışman onayı/reddi, ekle-sil süresi, idempotans", chkF3},
		{"F-4", "haftalık ders programı (çözücü) ve sınav takvimi (çakışma reddi)", chkF4},
		{"F-5", "not girişi: ağırlıklar, harf notu, bütünleme, itiraz", chkF5},
		{"F-6", "transkript PDF'i ve imzalı doğrulama", chkF6},
		{"F-7", "mezuniyet kontrolü ve 'kalan dersler' raporu", chkF7},
		{"F-8", "raporlama: bölüm başarısı, doluluk, GANO dağılımı", chkF8},
		{"T-1", "kayıt anı yarışı: kontenjan aşılmaz, bekleme listesi adil, otomatik atama", chkT1},
		{"T-2", "kural doğrulayıcı: ön koşul DAG, çakışma, AKTS, tüm ihlaller birlikte", chkT2},
		{"T-3", "müfredat sürümleme, denklik, muafiyet, zaman yolculuğu sorgusu", chkT3},
		{"T-4", "not motoru: bağıl/mutlak, bütünleme önceliği, GANO işçisi, tekrar politikası", chkT4},
		{"T-5", "ders programı çözücü: çakışmasız program, çözümsüzlükte kısıt raporu", chkT5},
		{"T-6", "mezuniyet motoru: veri olarak kurallar, en uygun havuz ataması", chkT6},
		{"T-7", "yetki, değiştirilemez denetim, çift onay, kilit, imzalı belge", chkT7},
		{"BONUS", "danışman için riskli öğrenci uyarısı (GANO düşüşü, kalan dersler)", chkBonusRisk},
	}
}

func chkHealth(t *T, e *env) {
	r := e.call("GET", "/health", "", nil)
	t.must(r.Status == 200 && r.str("status") == "ok" && r.str("database") == "ok", "/health: %d %s", r.Status, r.Raw)
	m := e.call("GET", "/metrics", "", nil)
	t.must(m.Status == 200 && bytes.Contains(m.Raw, []byte("uninet_http_requests_total")), "/metrics: %d", m.Status)
	nf := e.call("GET", "/yok", "", nil)
	t.must(nf.Status == 404 && nf.str("error.code") == "not_found", "bilinmeyen yol JSON hata vermedi: %d %s", nf.Status, nf.Raw)
}

func chkAuth(t *T, e *env) {
	bad := e.call("POST", "/auth/login", "", M{"email": "ogrenci@uninet.test", "password": "yanlis"})
	t.must(bad.Status == 401 && bad.str("error.code") == "invalid_credentials", "yanlış parola: %d %s", bad.Status, bad.Raw)
	unk := e.call("POST", "/auth/login", "", M{"email": "yok@uninet.test", "password": "yanlis"})
	t.must(unk.Status == 401 && unk.str("error.message") == bad.str("error.message"),
		"olmayan kullanıcı farklı yanıt veriyor (kullanıcı varlığı sızıyor): %s", unk.Raw)
	t.must(e.call("POST", "/auth/login", "", M{}).Status == 400, "boş giriş gövdesi 400 olmalı")

	t.must(e.call("GET", "/me", "", nil).Status == 401, "token'sız /me 401 olmalı")
	t.must(e.call("GET", "/me", "çöp", nil).Status == 401, "çöp token 401 olmalı")
	// alg=none ile imzasız sahte token: doğru formatta ama imzasız, reddedilmeli.
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	forged := enc(`{"alg":"none","typ":"JWT"}`) + "." + enc(fmt.Sprintf(`{"sub":"1","role":"registrar","exp":%d}`, time.Now().Add(time.Hour).Unix())) + "."
	t.must(e.call("GET", "/me", forged, nil).Status == 401, "alg=none token kabul edildi")

	access, refresh, st := e.login("ogrenci@uninet.test", "Demo1234!")
	t.must(st == 200, "demo öğrenci girişi: %d", st)
	me := e.call("GET", "/me", access, nil)
	t.must(me.Status == 200 && me.str("role") == "student", "/me: %d %s", me.Status, me.Raw)

	ref := e.call("POST", "/auth/refresh", "", M{"refresh_token": refresh})
	t.must(ref.Status == 200 && ref.str("access_token") != "" && ref.str("refresh_token") != refresh, "refresh: %d %s", ref.Status, ref.Raw)
	t.must(e.call("POST", "/auth/refresh", "", M{"refresh_token": refresh}).Status == 401, "kullanılmış refresh token yeniden kabul edildi")
	t.must(e.call("POST", "/auth/refresh", "", M{"refresh_token": ref.str("refresh_token")}).Status == 401,
		"yeniden kullanım tespitinden sonra zincirdeki yeni token hâlâ geçerli (tüm oturumlar kapanmalıydı)")

	_, r2, _ := e.login("ogrenci@uninet.test", "Demo1234!")
	t.must(e.call("POST", "/auth/logout", "", M{"refresh_token": r2}).Status == 204, "logout 204 olmalı")
	t.must(e.call("POST", "/auth/refresh", "", M{"refresh_token": r2}).Status == 401, "logout sonrası refresh çalıştı")
}

func chkAuthz(t *T, e *env) {
	t.must(e.call("POST", "/faculties", e.stu, M{"code": "X", "name": "Y"}).Status == 403, "öğrenci fakülte oluşturabildi")
	t.must(e.call("POST", "/rooms", e.instr, M{"code": "X", "capacity": 10, "kind": "lecture"}).Status == 403, "öğretim üyesi derslik oluşturabildi")
	t.must(e.call("GET", "/reports/occupancy?term_id=1", e.stu, nil).Status == 403, "öğrenci rapor görebildi")
	t.must(e.call("GET", "/reports/occupancy?term_id=1", e.instr, nil).Status == 403, "öğretim üyesi rapor görebildi")
	t.must(e.call("POST", "/terms", e.adv, M{"code": "X", "name": "Y"}).Status == 403, "danışman dönem oluşturabildi")

	// Başka bir öğrencinin sonuçları.
	other := e.newStudent(t, "authz", e.world().prog, 2025, nil)
	t.must(e.call("GET", p("/students/%d/results", other.sid), e.stu, nil).Status == 403, "öğrenci başkasının sonuçlarını görebildi")
	t.must(e.call("GET", p("/students/%d/results", other.sid), e.rep, nil).Status == 403, "raporlama rolü bireysel sonuçları görebildi")
	t.must(e.call("GET", p("/students/%d/results", other.sid), other.token, nil).Status == 200, "öğrenci kendi sonuçlarını göremedi")

	// Girdi doğrulaması.
	v := e.call("POST", "/faculties", e.reg, M{})
	t.must(v.Status == 400 && v.str("error.code") == "validation_error", "boş gövde 400 validation_error olmalı: %d %s", v.Status, v.Raw)
	u := e.call("POST", "/faculties", e.reg, M{"code": "X", "name": "Y", "bilinmeyen": 1})
	t.must(u.Status == 400, "bilinmeyen alan reddedilmedi: %d", u.Status)
	t.must(e.call("GET", "/faculties?limit=0", e.reg, nil).Status == 400, "geçersiz limit kabul edildi")
}

func chkF1(t *T, e *env) {
	w := e.world()
	post := func(path string, body M) R {
		r := e.call("POST", path, e.reg, body)
		t.must(r.Status == 201, "POST %s: %d %s", path, r.Status, r.Raw)
		return r
	}
	w.fac = post("/faculties", M{"code": "VF" + e.sfx, "name": "Doğrulama Fakültesi"}).id("id")
	w.dept = post("/departments", M{"faculty_id": w.fac, "code": "VD" + e.sfx, "name": "Doğrulama Bölümü"}).id("id")
	w.prog = post("/programs", M{"department_id": w.dept, "code": "VP" + e.sfx, "name": "Doğrulama Programı", "total_ects_required": 240}).id("id")
	for _, c := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		w.course[c] = post("/courses", M{"code": "V" + c + e.sfx, "name": "Ders " + c, "ects": 5, "department_id": w.dept}).id("id")
	}

	// İki müfredat sürümü: 2020 (A,B,C,D) ve 2024 (A,B,E,F).
	mk := func(year int, courses ...string) int64 {
		var items []M
		for i, c := range courses {
			items = append(items, M{"course_id": w.course[c], "semester": i/2 + 1, "kind": "required"})
		}
		r := post(p("/programs/%d/curriculum-versions", w.prog), M{"effective_year": year, "courses": items})
		t.must(len(r.arr("courses")) == len(courses), "sürüm dersleri: %s", r.Raw)
		return r.id("id")
	}
	w.v2020 = mk(2020, "A", "B", "C", "D")
	w.v2024 = mk(2024, "A", "B", "E", "F")

	// Kısıtlar: aynı kod, olmayan referans, aynı yıl.
	t.must(e.call("POST", "/faculties", e.reg, M{"code": "VF" + e.sfx, "name": "x"}).Status == 409, "aynı kodlu fakülte kabul edildi")
	t.must(e.call("POST", "/departments", e.reg, M{"faculty_id": 999999999, "code": "Z" + e.sfx, "name": "x"}).Status == 422, "olmayan fakülteye bölüm kabul edildi")
	t.must(e.call("POST", p("/programs/%d/curriculum-versions", w.prog), e.reg, M{"effective_year": 2020, "courses": []M{{"course_id": w.course["A"], "semester": 1, "kind": "required"}}}).Status == 409,
		"aynı yıla ikinci müfredat sürümü kabul edildi")
	t.must(e.call("POST", p("/programs/%d/curriculum-versions", w.prog), e.reg, M{"effective_year": 2030, "courses": []M{
		{"course_id": w.course["A"], "semester": 1, "kind": "required"}, {"course_id": w.course["A"], "semester": 2, "kind": "required"}}}).Status == 400, "müfredatta tekrarlayan ders kabul edildi")

	// Keyset sayfalama: limit=1 → bir öğe + next_after; son sayfada null.
	page := e.call("GET", p("/programs/%d/curriculum-versions?limit=1", w.prog), e.stu, nil)
	t.must(page.Status == 200 && len(page.arr("items")) == 1 && page.val("next_after") != nil, "sayfa 1: %s", page.Raw)
	page2 := e.call("GET", p("/programs/%d/curriculum-versions?limit=1&after=%d", w.prog, int64(page.num("next_after"))), e.stu, nil)
	t.must(len(page2.arr("items")) == 1 && page2.val("next_after") == nil, "sayfa 2 (son): %s", page2.Raw)
	ver := e.call("GET", p("/curriculum-versions/%d", w.v2020), e.stu, nil)
	t.must(ver.Status == 200 && len(ver.arr("courses")) == 4, "sürüm ayrıntısı: %s", ver.Raw)

	// Kullanıcılar: iki öğretim üyesi ve bir danışman (sonraki kontroller kullanır).
	var err error
	w.instrA, err = e.newUser("instructor", "hocaA")
	t.must(err == nil, "öğretim üyesi A: %v", err)
	w.instrB, err = e.newUser("instructor", "hocaB")
	t.must(err == nil, "öğretim üyesi B: %v", err)
	w.advisor, err = e.newUser("advisor", "danisman")
	t.must(err == nil, "danışman: %v", err)
}

func chkF2(t *T, e *env) {
	w := e.world()
	term := e.call("POST", "/terms", e.reg, M{"code": "VT" + e.sfx, "name": "Doğrulama Güz", "registration_open": true})
	t.must(term.Status == 201, "dönem: %d %s", term.Status, term.Raw)
	w.term = term.id("id")
	for i, c := range []struct {
		code string
		cap  int
		kind string
	}{{"V1", 60, "lecture"}, {"V2", 60, "lecture"}, {"V3", 80, "lecture"}, {"VL", 30, "lab"}} {
		r := e.call("POST", "/rooms", e.reg, M{"code": c.code + e.sfx, "capacity": c.cap, "kind": c.kind})
		t.must(r.Status == 201, "derslik %d: %d %s", i, r.Status, r.Raw)
	}
	bad := e.call("POST", "/rooms", e.reg, M{"code": "X" + e.sfx, "capacity": 0, "kind": "lecture"})
	t.must(bad.Status == 400, "kapasitesi 0 olan derslik kabul edildi")

	// Şube: kontenjan 2, öğretim üyesi A; ders saati ve çakışan kayıt olmayan ikinci şube kontrolü.
	sec := e.call("POST", "/sections", e.reg, M{"term_id": w.term, "course_id": w.course["A"], "code": "01", "instructor_id": w.instrA.id, "capacity": 2})
	t.must(sec.Status == 201, "şube: %d %s", sec.Status, sec.Raw)
	w.sections["A"] = sec.id("id")
	t.must(e.call("POST", "/sections", e.reg, M{"term_id": w.term, "course_id": w.course["A"], "code": "01", "capacity": 2}).Status == 409, "aynı (dönem,ders,kod) şube kabul edildi")
	t.must(e.call("POST", "/sections", e.reg, M{"term_id": w.term, "course_id": w.course["A"], "code": "02", "capacity": 0}).Status == 400, "kontenjan 0 kabul edildi")
	mt := e.call("POST", p("/sections/%d/meetings", w.sections["A"]), e.reg, M{"day": 1, "start_min": 540, "end_min": 660})
	t.must(mt.Status == 201, "ders saati: %d %s", mt.Status, mt.Raw)
	t.must(e.call("POST", p("/sections/%d/meetings", w.sections["A"]), e.reg, M{"day": 1, "start_min": 700, "end_min": 600}).Status == 400, "bitişi başlangıçtan önce olan saat kabul edildi")

	got := e.call("GET", p("/sections/%d", w.sections["A"]), e.stu, nil)
	t.must(got.num("capacity") == 2 && got.num("enrolled_count") == 0 && got.num("waitlist_size") == 0, "şube durumu: %s", got.Raw)
	list := e.call("GET", p("/sections?term_id=%d", w.term), e.stu, nil)
	t.must(len(list.arr("items")) == 1, "dönemin şube listesi: %s", list.Raw)
	t.must(e.call("POST", "/sections", e.stu, M{"term_id": w.term, "course_id": w.course["A"], "code": "09", "capacity": 5}).Status == 403, "öğrenci şube oluşturdu")
}

func chkF3(t *T, e *env) {
	w := e.world()
	s := make([]student, 3)
	for i := range s {
		s[i] = e.newStudent(t, p("f3-%d", i), w.prog, 2025, &w.advisor)
	}
	secA := w.sections["A"]

	// Idempotency-Key zorunlu.
	t.must(e.call("POST", p("/sections/%d/enrollments", secA), s[0].token, nil).Status == 400, "Idempotency-Key'siz kayıt kabul edildi")

	r1 := e.enroll(s[0].token, secA, "f3-"+e.sfx+"-0")
	t.must(r1.Status == 201 && r1.str("status") == "enrolled", "1. kayıt: %d %s", r1.Status, r1.Raw)
	replay := e.enroll(s[0].token, secA, "f3-"+e.sfx+"-0")
	t.must(replay.Status == 201 && replay.id("enrollment_id") == r1.id("enrollment_id") && replay.Header.Get("Idempotent-Replayed") == "true",
		"aynı Idempotency-Key aynı sonucu vermedi: %s", replay.Raw)
	t.must(e.enroll(s[0].token, secA, "f3-"+e.sfx+"-0b").Status == 409, "aynı şubeye ikinci kayıt kabul edildi")
	r2 := e.enroll(s[1].token, secA, "f3-"+e.sfx+"-1")
	t.must(r2.Status == 201 && r2.str("status") == "enrolled", "2. kayıt: %s", r2.Raw)
	r3 := e.enroll(s[2].token, secA, "f3-"+e.sfx+"-2")
	t.must(r3.Status == 201 && r3.str("status") == "waitlisted" && r3.num("waitlist_position") == 1, "3. kayıt bekleme listesine girmeli: %s", r3.Raw)
	sc := e.call("GET", p("/sections/%d", secA), s[0].token, nil)
	t.must(sc.num("enrolled_count") == 2 && sc.num("waitlist_size") == 1, "şube sayaçları: %s", sc.Raw)

	// Danışman: yalnızca kendi danışanlarının bekleyen kayıtlarını görür; onaylar ve reddeder.
	pend := e.call("GET", "/advisor/enrollments?status=pending", w.advisor.token, nil)
	t.must(pend.Status == 200 && len(pend.arr("items")) >= 3, "danışman bekleyen kayıtlar: %s", pend.Raw)
	t.must(e.call("GET", "/advisor/enrollments", e.adv, nil).Status == 200, "demo danışman listesi")
	t.must(e.call("POST", p("/enrollments/%d/advisor-decision", r1.id("enrollment_id")), e.adv, M{"approve": true}).Status == 404,
		"başka danışman, danışanı olmayan kayıt için karar verebildi")
	ok := e.call("POST", p("/enrollments/%d/advisor-decision", r1.id("enrollment_id")), w.advisor.token, M{"approve": true})
	t.must(ok.Status == 200 && ok.str("advisor_status") == "approved", "danışman onayı: %s", ok.Raw)
	rej := e.call("POST", p("/enrollments/%d/advisor-decision", r2.id("enrollment_id")), w.advisor.token, M{"approve": false})
	t.must(rej.Status == 200 && rej.id("promoted_enrollment_id") == r3.id("enrollment_id"), "ret, bekleme listesinin ilk kişisine koltuk vermeli: %s", rej.Raw)
	mine := e.call("GET", "/me/enrollments", s[2].token, nil)
	t.must(len(mine.arr("items")) == 1 && mine.arr("items")[0].(M)["status"] == "enrolled", "terfi eden öğrenci kayıtlı görünmüyor: %s", mine.Raw)

	// Ekle-sil süresi: süre dolunca öğrenci bırakamaz, yönetici süreyi kaldırınca bırakabilir.
	t.must(e.call("PUT", p("/terms/%d/drop-deadline", w.term), e.reg, M{"deadline": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}).Status == 200, "süre ayarı")
	d := e.call("DELETE", p("/enrollments/%d", r1.id("enrollment_id")), s[0].token, nil)
	t.must(d.Status == 409 && d.str("error.code") == "drop_period_over", "süre dolmuşken bırakma: %d %s", d.Status, d.Raw)
	t.must(e.call("PUT", p("/terms/%d/drop-deadline", w.term), e.reg, M{"deadline": nil}).Status == 200, "süre kaldırma")
	t.must(e.call("DELETE", p("/enrollments/%d", r1.id("enrollment_id")), s[0].token, nil).Status == 200, "süre yokken bırakma")
	t.must(e.call("DELETE", p("/enrollments/%d", r1.id("enrollment_id")), s[0].token, nil).Status == 200, "tekrar bırakma zararsız olmalı (idempotent)")
	t.must(e.call("DELETE", p("/enrollments/%d", r1.id("enrollment_id")), s[1].token, nil).Status == 404, "başkasının kaydı bırakılabildi")
	t.note("kayıt → bekleme → ret ile otomatik atama → ekle-sil süresi: tamam")
}
