# TESTING.md — Hızlı doğrulama kılavuzu

Bu belge, projeyi teslim sonrası kısa sürede kontrol edecek kişi içindir. Her kapsam maddesi (`F-1`…`F-8`) ve her
zorlu görev (`T-1`…`T-7`) için **nasıl denenir** ve **beklenen sonuç** aşağıdadır.

## 0. Tek komutla doğrulama

```bash
cp .env.example .env
docker compose up --build -d          # sistem + otomatik migrasyon + seed
make verify                           # eşdeğeri: docker compose --profile verify run --rm verify
```

`make verify` ayakta olan sistem üzerinde **her kimliği API üzerinden uçtan uca** dener ve sonunda `PASS/FAIL`
tablosu basar; herhangi biri başarısızsa çıkış kodu `1` olur. Süre: yaklaşık 10 saniye (şartname sınırı: 15 dakika).
Her koşu kendi benzersiz verisini kurduğu için art arda çalıştırılabilir; ilk haline dönmek için `make reset-demo`.

> Windows'ta `make` yoksa şu komut aynı işi yapar: `docker compose --profile verify run --rm verify`

Örnek çıktı (kısaltılmış):

```
KİMLİK   SONUÇ  AÇIKLAMA
HEALTH   PASS   GET /health (veritabanı dahil), /metrics, JSON 404
AUTH     PASS   yanlış parola, token'sız/sahte erişim, refresh rotasyonu + yeniden kullanım tespiti
F-1 … F-8  PASS
T-1 … T-7  PASS
Toplam 18 kimlik: 18 PASS, 0 FAIL
```

Diğer komutlar:

| Komut | Ne yapar |
|---|---|
| `make reset-demo` | Verileri ilk seed haline döndürür (volume silinir, migrasyon + seed yeniden çalışır) |
| `make test` | Go testleri (birim + gerçek PostgreSQL'e karşı entegrasyon), konteynerde |
| `make load` | **T-1 yük testi:** 12.000 öğrenci seed → k6 (5.000 öğrenci, 15.000 istek) → veritabanı doğrulaması |

## 1. Demo hesapları (her rol için bir tane)

Tüm hesapların parolası: **`Demo1234!`**

| Rol | E-posta | Arayüzde göreceği |
|---|---|---|
| Öğrenci | `ogrenci@uninet.test` | Ders kaydı, programım, notlarım, transkript, mezuniyet durumu |
| Öğretim üyesi | `hoca@uninet.test` | Şubelerim, not girişi, ilan, itirazlar, düzeltme talebi |
| Danışman | `danisman@uninet.test` | Danışanlarımın kayıt onayları |
| Bölüm sekreteri | `sekreter@uninet.test` | Akademik yapı, dönem/şube, program çözücü, düzeltme onayı, raporlar |
| Öğrenci işleri | `ogrenciisleri@uninet.test` | Sekreterin tümü + muafiyet, tekrar-alma politikası |
| Raporlama (rektörlük) | `rektorluk@uninet.test` | Bölüm başarısı, doluluk, GANO dağılımı |

Arayüz: <http://localhost:3000> · API: <http://localhost:8080> · Sağlık: <http://localhost:8080/health> ·
Metrikler: <http://localhost:8080/metrics> · OpenAPI: <http://localhost:8080/openapi.yaml>

## 2. Kimlik kimlik: nasıl denenir / beklenen sonuç

`requests.http` (VS Code REST Client) içindeki istekler bu tabloyla aynı sırayı izler; her satır için kanıt komutu da verilmiştir.

### Kapsam maddeleri

| Kimlik | Nasıl denenir | Beklenen sonuç | Kanıt komutu |
|---|---|---|---|
| `F-1` Akademik yapı | Öğrenci işleriyle: `POST /faculties` → `/departments` → `/programs` → `/courses` → `POST /programs/{id}/curriculum-versions`. `GET …?limit=1` ile sayfalama. | 201; aynı kod 409; olmayan referans 422; aynı yıla ikinci sürüm 409; son sayfada `next_after: null`. | `make verify` → `F-1` |
| `F-2` Dönem ve şube | `POST /terms`, `POST /rooms`, `POST /sections` (kontenjan, öğretim üyesi), `POST /sections/{id}/meetings`. | Şube `capacity/enrolled_count/waitlist_size` döner; kontenjan 0 → 400; aynı (dönem,ders,kod) → 409. | `F-2` |
| `F-3` Ders kaydı | Öğrenciyle `POST /sections/{id}/enrollments` (`Idempotency-Key` zorunlu). Kontenjan dolunca bekleme. Danışmanla `POST /enrollments/{id}/advisor-decision`. `PUT /terms/{id}/drop-deadline` ile ekle-sil süresi. | Dolunca `waitlisted` + sıra; ret → koltuk bekleyen ilk kişiye geçer; süre dolunca öğrenci bırakamaz (409). Aynı anahtar aynı yanıtı verir (`Idempotent-Replayed: true`). | `F-3` |
| `F-4` Program + sınav | `POST /terms/{id}/schedule/solve {"apply":true}`; `GET /terms/{id}/schedule`. `POST /sections/{id}/exams`. | Çakışmasız program; sınav oda/öğretim üyesi/öğrenci çakışması 409 ve hepsi birlikte listelenir. | `F-4` |
| `F-5` Not girişi | `PUT /sections/{id}/grade-components`, `PUT …/scores` (normal/bütünleme/mazeret), `POST …/grades/publish`; öğrenci `POST /course-results/{id}/objections`. | Harf notu ağırlıklı hesaplanır; bütünleme normal finalin yerine geçer; itiraz kabulü notu **tek başına değiştirmez**. | `F-5` |
| `F-6` Transkript | `POST /students/{id}/transcripts` → `GET /documents/{id}/pdf` → `GET /verify/{kod}` (kimlik gerekmez). | PDF + QR; doğrulama `valid:true`; kodun bir karakteri bozulursa 404. | `F-6` |
| `F-7` Mezuniyet | `PUT /curriculum-versions/{id}/graduation-rules`; `GET /students/{id}/graduation-audit`. | `can_graduate` ya da eksik dersler + `remaining_required_courses` listesi. | `F-7` |
| `F-8` Raporlama | Raporlama hesabıyla `GET /reports/department-success?term_id=`, `/reports/occupancy?term_id=`, `/reports/gano-distribution`. | Bölüm bazlı başarı oranı, doluluk, GANO aralık dağılımı; öğrenci rolü 403. | `F-8` |

### Zorlu görevler

| Kimlik | Nasıl denenir | Beklenen sonuç | Kanıt komutu |
|---|---|---|---|
| `T-1` Kayıt anı yarışı | **Hızlı:** `make verify` (60 eşzamanlı istek, kontenjan 10). **Tam:** `make load` (5.000 öğrenci, 15.000 istek, k6) | Tam kontenjan kadar `enrolled`, kalanı `waitlisted`; sıra adil; boşalan koltuk sıradakine otomatik; **veritabanı doğrulaması PASS**. Sonuçlar: [`docs/LOAD_TEST.md`](docs/LOAD_TEST.md) | `make load` · `go test ./internal/registration -run Concurrent` |
| `T-2` Kural doğrulayıcı | `POST /me/enrollments/validate {"section_ids":[…]}`; `PUT /courses/{id}/prerequisites`. | Ön koşul + tekrar + çakışma + AKTS ihlalleri **tek yanıtta birlikte**; ön koşul döngüsü 422 `prerequisite_cycle`; `A ve (B veya C)` desteklenir. | `go test ./internal/rules` (%98 kapsam, fuzz) |
| `T-3` Sürümleme/denklik | `GET /students/{id}/curriculum`; yeni sürüm ekleyip tekrar sorgula; `POST /course-equivalences`; `GET /students/{id}/required-courses?as_of=2024`. | Eski girişli öğrenci yeni müfredattan **etkilenmez**; denklik zorunlu dersi karşılar; zaman yolculuğu doğru yıl ve dersleri döner. | `T-3` |
| `T-4` Not motoru | `PUT …/grade-components {"method":"relative"}` + ilan; `PUT /programs/{id}/retake-policy {"policy":"best"}`. | Bağıl (T-skoru) harf notları tekdüze; GANO arka plan işçisiyle yakınsar (`gano_cached == gano`); politika değişince tüm öğrenciler yeniden hesaplanır. | `go test ./internal/grading ./internal/gradebook` |
| `T-5` Program çözücü | `POST /terms/{id}/schedule/solve`; öğretim üyesine `PUT /instructors/{id}/unavailability` ile bütün hafta engel koy. | Çakışmasız program; çözümsüzlükte `conflicts[].kind` (örn. `INSTRUCTOR_UNAVAILABLE`) ve Türkçe açıklama. **60 ders/12 derslik/20 öğretim üyesi:** ms düzeyinde (hedef 30 sn). | `go test -v -run TestSample ./internal/scheduler` |
| `T-6` Mezuniyet motoru | Aynı dersi iki havuzda tanımla; `GET …/graduation-audit`; kuralı JSON ile değiştir. | Ders yalnızca **bir** havuza sayılır, en uygun atama bulunur (kaba kuvvetle doğrulandı); kural değişince kod değişmeden sonuç değişir. | `go test ./internal/graduation` (%100 çözücü kapsamı) |
| `T-7` Yetki/denetim/belge | Başka şubeye not girmeyi dene (403); ilandan sonra giriş (409); kendi düzeltme talebini onayla (403); `psql` ile `UPDATE audit_log` dene. | Öğretim üyesi yalnız kendi şubesi, danışman yalnız danışanları; ilan sonrası kilit; **çift onay**; denetim kaydı **veritabanı düzeyinde** değiştirilemez; HMAC imzalı QR. | `T-7` · `go test ./internal/documents` |

### Ek senaryolar (her koşuda denenir)

| Kimlik | Kapsam |
|---|---|
| `HEALTH` | `GET /health` veritabanı bağlantısını da denetler; `GET /metrics` Prometheus metrikleri; bilinmeyen yol JSON 404 |
| `AUTH` | Yanlış parola 401 (olmayan kullanıcıyla aynı yanıt → kullanıcı varlığı sızmaz); `alg=none` sahte JWT 401; refresh rotasyonu; **kullanılmış refresh token yeniden gelirse tüm oturumlar kapanır** |
| `AUTHZ` | Öğrenci fakülte oluşturamaz / başkasının sonucunu göremez (403); raporlama bireysel sonuç göremez; boş gövde 400; bilinmeyen alan 400 |

## 3. Arayüzde elle denenecek kısa senaryolar

<http://localhost:3000> adresinden demo hesaplarla (giriş ekranındaki kısayol düğmeleri parolayı doldurur):

1. **Öğrenci → ders kaydı ve bekleme listesi.** `ogrenci@uninet.test` ile gir → *Ders Kaydı* → `BM301` (kontenjan 2) için **Kayıt ol**. Durum rozeti "Kayıtlı" ya da "Bekleme #n" olur. Aynı derse tekrar bas: "zaten kayıtlı" uyarısı çıkar.
2. **Öğrenci → kural ihlallerinin hepsi birlikte.** *Ders Kaydı* sayfasında `BM102` ve `BM101` + `MAT101` şubelerini sepete ekleyip **Kuralları denetle**: ön koşul eksikliği ve ders saati çakışması **aynı listede** görünür.
3. **Danışman → onay/ret.** `danisman@uninet.test` → *Onaylar* → öğrencinin kaydını reddet; boşalan koltuk bekleyen ilk öğrenciye geçer (öğrenci hesabında "Kayıtlı" olur).
4. **Öğretim üyesi → not girişi.** `hoca@uninet.test` → *Şubelerim* → bir şube → bileşenleri ve puanları gir → **Notları ilan et**. İlandan sonra giriş alanları kilitlenir.
5. **Öğrenci → transkript ve doğrulama.** *Notlarım* → **Transkript PDF'i oluştur**. PDF'teki QR/bağlantı `/verify/…` sayfasında "Geçerli" gösterir; kodu bozarsan "Doğrulanamadı".
6. **Sekreter → program çözücü.** `sekreter@uninet.test` → *Program* → **Çöz**: program tablosu ya da çatışan kısıtların listesi görünür.
7. **Raporlama.** `rektorluk@uninet.test` → *Raporlar*: bölüm başarısı, doluluk ve GANO dağılımı.
8. **Her ekranda:** yükleme (iskelet), boş durum ve hata durumu görünür; telefon genişliğinde (≤ 480 px) menü açılır-kapanır ve tablolar yatay kayar.

## 4. Kanıt komutları (görev kimliğine göre)

| Kimlik | Komut |
|---|---|
| T-1 | `make load` → çıktıda `PASS: kontenjan aşımı yok…` ve k6 özeti. Yarış testi: `docker run … go test ./internal/registration -run TestConcurrentEnroll`. Kilidi kaldırınca testin **kırıldığı** DECISIONS.md #1'de belgelidir. |
| T-2 | `go test -cover ./internal/rules` · fuzz: `go test -fuzz FuzzNodeJSON ./internal/rules` |
| T-3 | `make verify` (T-3) · `go test ./internal/rules -run 'ExpandPassed|CourseStatus|StudyYear'` |
| T-4 | `go test ./internal/grading ./internal/gradebook` (özellik testleri: harf tekdüzeliği, best ≥ last) |
| T-5 | `go test -v -run 'TestSample|TestSolverAgrees' ./internal/scheduler` (kaba kuvvetle çapraz doğrulama) |
| T-6 | `go test -run TestSolverMatchesBruteForce ./internal/graduation` |
| T-7 | `go test ./internal/documents` (imza kurcalama testi) · `make verify` (T-7) |

Go yerelde kurulu olmak zorunda değildir; `make test` testleri konteynerde çalıştırır.
