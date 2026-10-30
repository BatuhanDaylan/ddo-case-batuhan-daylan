package gradebook

import (
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/batuhan-daylan/uninet/internal/auth"
)

// F-5: not itirazı. İtiraz kabul edilse bile not, ikinci kişi onaylayana kadar DEĞİŞMEZ.
func TestObjectionFlowNeedsSecondApprover(t *testing.T) {
	e := setup(t, 2)
	// Router'a itiraz rotalarını ekle (setup yalnızca ana rotaları bağlar).
	r := chi.NewRouter()
	r.Use(e.iss.Authenticate)
	h := NewHandler(e.pool)
	h.Routes(r)
	h.ObjectionRoutes(r)
	e.router = r

	e.setComponents(t, "absolute")
	instr := e.token(auth.RoleInstructor)
	e.do(t, "PUT", path("/sections/%d/scores", e.section), instr, map[string]any{"entries": []any{
		e.score(t, 0, "midterm", "regular", 60), e.score(t, 0, "final", "regular", 60), // 60 → DC
		e.score(t, 1, "midterm", "regular", 90), e.score(t, 1, "final", "regular", 90)}})
	_, pub := e.do(t, "POST", path("/sections/%d/grades/publish", e.section), instr, nil)
	res := pub["results"].([]any)
	resultID0 := int64(res[0].(map[string]any)["result_id"].(float64))
	resultID1 := int64(res[1].(map[string]any)["result_id"].(float64))

	tokOf := func(i int) string {
		uid := mustScan[int64](t, e.pool, `SELECT user_id FROM students WHERE id=$1`, e.students[i])
		tok, _ := e.iss.Access(uid, auth.RoleStudent)
		return tok
	}
	// Bu test student token'ı ile istek atar; e.do rol belirteci yerine hazır token alır.
	do := func(method, p, token string, body any) (int, map[string]any) { return e.do(t, method, p, token, body) }
	url := path("/course-results/%d/objections", resultID0)

	// Başkasının sonucuna itiraz edilemez; geçersiz gövde reddedilir.
	if c, _ := do("POST", path("/course-results/%d/objections", resultID1), tokOf(0), map[string]any{"reason": "x"}); c != 404 {
		t.Errorf("başkasının sonucuna itiraz: %d, beklenen 404", c)
	}
	if c, _ := do("POST", url, tokOf(0), map[string]any{"reason": "  "}); c != 400 {
		t.Errorf("boş gerekçe: %d, beklenen 400", c)
	}
	if c, _ := do("POST", url, instr, map[string]any{"reason": "x"}); c != 403 {
		t.Errorf("öğretim üyesi itiraz açtı: %d", c)
	}
	code, body := do("POST", url, tokOf(0), map[string]any{"reason": "Vize kâğıdım yanlış okunmuş"})
	if code != 201 {
		t.Fatalf("itiraz: %d %v", code, body)
	}
	objID := int64(body["id"].(float64))
	if c, _ := do("POST", url, tokOf(0), map[string]any{"reason": "ikinci"}); c != 409 {
		t.Errorf("ikinci açık itiraz: %d, beklenen 409", c)
	}

	// Öğretim üyesi kendi şubesinin itirazlarını görür, başkası göremez.
	if c, p := do("GET", path("/sections/%d/objections", e.section), instr, nil); c != 200 || len(p["items"].([]any)) != 1 {
		t.Errorf("şube itirazları: %d %v", c, p)
	}
	if c, _ := do("GET", path("/sections/%d/objections", e.section), e.token("other_instructor"), nil); c != 403 {
		t.Errorf("başka öğretim üyesi itirazları gördü: %d", c)
	}

	dec := path("/grade-objections/%d/decision", objID)
	if c, _ := do("POST", dec, e.token("other_instructor"), map[string]any{"accept": true, "new_letter": "BB", "response": "x"}); c != 403 {
		t.Errorf("başka öğretim üyesi karar verdi: %d", c)
	}
	if c, _ := do("POST", dec, instr, map[string]any{"accept": true, "response": "kabul"}); c != 400 {
		t.Errorf("new_letter olmadan kabul: %d, beklenen 400", c)
	}
	code, body = do("POST", dec, instr, map[string]any{"accept": true, "new_letter": "BB", "response": "Maddi hata doğrulandı"})
	if code != 200 || body["status"] != "accepted" || body["correction_id"] == nil {
		t.Fatalf("kabul: %d %v", code, body)
	}
	// Not hâlâ DC: kabul, notu tek başına değiştirmez.
	if l := mustScan[string](t, e.pool, `SELECT letter FROM course_results WHERE id=$1`, resultID0); l != "DC" {
		t.Errorf("itiraz kabulüyle not tek başına değişti: %s", l)
	}
	cid := int64(body["correction_id"].(float64))
	if n := mustScan[int](t, e.pool, `SELECT count(*) FROM grade_corrections WHERE id=$1 AND status='pending'`, cid); n != 1 {
		t.Error("kabulden bekleyen düzeltme talebi oluşmadı")
	}
	if c, _ := do("POST", dec, instr, map[string]any{"accept": false, "response": "tekrar"}); c != 409 {
		t.Errorf("karara bağlanmış itirazı yeniden karara bağlama: %d, beklenen 409", c)
	}

	// İkinci kişi (öğrenci işleri) onaylayınca not değişir.
	if c, b := do("POST", path("/grade-corrections/%d/decision", cid), e.token(auth.RoleRegistrar), map[string]any{"approve": true}); c != 200 {
		t.Fatalf("düzeltme onayı: %d %v", c, b)
	}
	if l := mustScan[string](t, e.pool, `SELECT letter FROM course_results WHERE id=$1`, resultID0); l != "BB" {
		t.Errorf("onay sonrası not %s, beklenen BB", l)
	}

	// Öğrenci kendi itirazlarını listeler; ret yolu da çalışır.
	if c, p := do("GET", "/me/objections", tokOf(0), nil); c != 200 || len(p["items"].([]any)) != 1 {
		t.Errorf("benim itirazlarım: %d %v", c, p)
	}
	_, b2 := do("POST", path("/course-results/%d/objections", resultID1), tokOf(1), map[string]any{"reason": "Notumdan memnun değilim"})
	if c, b := do("POST", path("/grade-objections/%d/decision", int64(b2["id"].(float64))), instr, map[string]any{"accept": false, "response": "Not doğru"}); c != 200 || b["status"] != "rejected" {
		t.Errorf("ret: %d %v", c, b)
	}
}
