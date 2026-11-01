# UniNet — Üniversite Öğrenci Bilgi Sistemi (OBS)

**Geliştirici: Batuhan Daylan** · DDO Yazılım Ekibi alım projesi ·
[![CI](https://github.com/BatuhanDaylan/ddo-case-batuhan-daylan/actions/workflows/ci.yml/badge.svg)](https://github.com/BatuhanDaylan/ddo-case-batuhan-daylan/actions/workflows/ci.yml)

Bir devlet üniversitesinin akademik yapısını, ders kayıt dönemlerini, not sürecini ve mezuniyet kontrolünü yöneten; **Go +
PostgreSQL + Docker** ile çalışan, **tek komutla ayağa kalkan** ve her zorlu görevi **otomatik doğrulayan** bir sistem.

> **Kısa sonuç:** 7 zorlu görevin hepsi ve 8 kapsam maddesinin hepsi uygulandı; `make verify` **19/19 PASS** (≈6 sn).
> Kayıt anı yarışı (T-1): 15.000 istek, 1.482 istek/sn, p95 426 ms, **0 sunucu hatası, tam 1.200 koltuk, kontenjan aşımı yok**.
> Yapılmayanlar ve sınırlamalar açıkça [§ Yapılmayanlar](#yapılmayanlar-ve-bilinen-sınırlamalar) altında.

## Hızlı başlangıç

```bash
git clone https://github.com/BatuhanDaylan/ddo-case-batuhan-daylan.git && cd ddo-case-batuhan-daylan
cp .env.example .env
docker compose up --build
```

| | Adres |
|---|---|
| **Arayüz** | <http://localhost:3000> |
| **API** | <http://localhost:8080> · sağlık `/health` · metrik `/metrics` · belge `/openapi.yaml` |
| Veritabanı | yalnızca compose ağında (host'a açılmaz) |

Migrasyon ve seed **otomatiktir**; elle adım yok. Servisler `healthcheck` içerir ve `depends_on: service_healthy` ile sıralanır.
Konteynerler root olmayan kullanıcıyla çalışır, imajlar çok aşamalıdır.

### Demo hesapları (her rol için bir tane) — parola: `Demo1234!`

| Rol | E-posta |
|---|---|
| Öğrenci | `ogrenci@uninet.test` |
| Öğretim üyesi | `hoca@uninet.test` |
| Danışman | `danisman@uninet.test` |
| Bölüm sekreteri | `sekreter@uninet.test` |
| Öğrenci işleri | `ogrenciisleri@uninet.test` |
| Raporlama (rektörlük) | `rektorluk@uninet.test` |

### Doğrulama komutları

```bash
make verify        # tüm F-/T- kimliklerini API üzerinden uçtan uca dener; PASS/FAIL tablosu (≈6 sn)
make test          # Go testleri (birim + gerçek PostgreSQL'e karşı entegrasyon)
make load          # T-1 yük testi: 12.000 öğrenci seed → k6 (5.000 öğrenci) → veritabanı doğrulaması
make reset-demo    # verileri ilk seed haline döndür
```

Windows'ta `make` yoksa: `docker compose --profile verify run --rm verify`. Ayrıntılı kılavuz: **[TESTING.md](TESTING.md)**.
Elle deneme: **[requests.http](requests.http)** (VS Code REST Client).

## Mimari özet

```
 Tarayıcı ──► nginx (3000, root değil) ──/api──► Go API (8080) ──pgx havuzu──► PostgreSQL 16
              React+Vite SPA                      │  chi · JWT/RBAC · idempotans         │ migrasyon (goose, gömülü)
                                                  │  GANO işçisi (arka plan)             │ CHECK/UNIQUE/FK kısıtları
                                                  │  /metrics · OpenTelemetry            │ tetikleyicilerle değiştirilemez
                                                  ▼                                      ▼ denetim/geçmiş/belge tabloları
                                   saf alan paketleri (veritabanı yok):
                                   rules · grading · graduation · scheduler · documents(imza)
```

**Katmanlama kararı:** iş kuralları **saf paketlerde** (`internal/rules`, `grading`, `graduation`, `scheduler`) yazıldı; bunlar
veritabanına dokunmaz, girdiyi bir yapı olarak alır. Böylece kapsam %94–99'a çıkar, kaba kuvvetle çapraz doğrulanabilir ve fuzz
testine girer. Veritabanı katmanı (`registration`, `gradebook`, `academic`...) yalnızca veriyi yükler, saf fonksiyonu çağırır ve
sonucu aynı transaction'da yazar.

| Paket | Sorumluluk |
|---|---|
| `registration` | kayıt, kontenjan, bekleme listesi, danışman onayı (T-1) |
| `rules` | ön koşul ifadeleri (`A ve (B veya C)`), döngü tespiti, çakışma/AKTS doğrulayıcı (T-2) |
| `academic` | fakülte→program→ders, müfredat sürümleri, denklik, zaman yolculuğu (F-1, T-3) |
| `grading` / `gradebook` | harf notu (mutlak/bağıl), GANO, ilan, çift onaylı düzeltme, itiraz, işçi (T-4, F-5) |
| `scheduler` / `timetable` | kısıt çözücü ve program/sınav API'si (T-5, F-4) |
| `graduation` | veri olarak mezuniyet kuralları, en uygun havuz ataması (T-6, F-7) |
| `documents` | HMAC imzalı transkript PDF'i + QR + `/verify` (T-7, F-6) |
| `auth` / `access` | argon2id, JWT+refresh rotasyonu, RBAC, erişim kuralları (T-7) |
| `reports` | bölüm başarısı, doluluk, GANO dağılımı (F-8) |
| `simulator` / `cmd/verify` | tohumlu veri üretici + yük, uçtan uca doğrulama aracı |

## Teknoloji seçimleri ve gerekçeleri

**Yığın (şartname gereği sabit):** Go 1.26 (chi, pgx), PostgreSQL 16, Docker Compose. Migrasyon: goose (binary'ye gömülü, açılışta uygulanır).

**Frontend: React 18 + TypeScript + Vite, nginx ile sunulur.** Gerekçe:
1. **Aynı kaynak (origin):** nginx `/api`'yi backend'e iletir; CORS ve ön-uçuş istekleri yoktur.
2. **Tipli API katmanı** (`strict` TypeScript) ve hızlı derleme.
3. **UI kütüphanesi yok:** tek CSS dosyası + 4 küçük bileşen. 6 rol için ~15 ekran tutarlı kalır, bağımlılık yüzeyi küçüktür.
4. Her ekran **yükleniyor / boş / hata** durumlarını ele alır, mobil genişlikte kullanılabilir, açık/koyu temalıdır. Sistemin canlı olduğu
   başlıktaki göstergede ve ana sayfadaki canlı panelde (sağlık + Prometheus sayaçları) görünür.

**Önemli tasarım kararları** (hepsi gerekçeleri ve **reddedilen alternatifleriyle** [DECISIONS.md](DECISIONS.md)'de; 27 kayıt):

| Karar | Özet |
|---|---|
| #1 Kontenjan = şube satır kilidi + CHECK | `SELECT … FOR UPDATE` hem aşımı hem adil sırayı sağlar; `CHECK` ikinci savunma hattıdır. Kilit kaldırılınca test **kırılır** (mutasyon kontrolü) |
| #3 İdempotans transaction içinde | Anahtar, işle aynı transaction'da yazılır: "anahtar yazıldı ama iş yapılmadı" durumu oluşamaz. Kayıtta zorunlu; transkript düzenlemede isteğe bağlı (aynı anahtar = aynı belge) |
| #4 Değiştirilemezlik veritabanında | `audit_log`, `grade_history`, `documents`: `UPDATE/DELETE/TRUNCATE` tetikleyiciyle engellenir (yetkili bağlantı dahil) |
| #8 Saf kural paketleri | Kapsam %94–99, fuzz, özellik tabanlı testler |
| #10 Kurallar şube kilidinden ÖNCE | Reddedilecek istek kritik bölümü hiç tutmaz (izde görünür: docs/TRACING.md) |
| #16 Belge imzası | `HMAC(id ‖ içerik özeti)`, JWT'den **ayrı anahtar**; veritabanı kurcalansa bile doğrulama başarısız olur |
| #18 Mezuniyet = DP çözücü | Açgözlü yöntem yanlış sonuç verir; akış (flow) AKTS+ders sayısını birlikte ifade edemez → kesin çözücü, kaba kuvvetle doğrulandı |
| #19 Program = bit maskeli CSP | Çözümsüzlükte **kesin analiz + arama tabanlı** çatışma raporu |
| #26 OpenAPI belgesi gerçek rotalarla eşleşmek ZORUNDA | Eşleşmeyen belge CI'ı kırar |

## Gereksinimlerin durumu

Her satır `make verify` ile uçtan uca denenir (kimlik = tablodaki ad). Ayrıntı ve komutlar: [TESTING.md](TESTING.md).

| Kimlik | Konu | Durum | Kanıt |
|---|---|---|---|
| F-1 | Akademik yapı, müfredat sürümleri | ✅ | `make verify` · keyset sayfalama |
| F-2 | Dönem ve şube yönetimi | ✅ | `make verify` |
| F-3 | Ders kaydı: danışman onayı, ekle-sil süresi, bekleme listesi | ✅ | `make verify` · idempotans |
| F-4 | Haftalık program ve sınav takvimi | ✅ | çözücü + sınav çakışma reddi (oda/öğretim üyesi/öğrenci) |
| F-5 | Not girişi, bütünleme, itiraz | ✅ | itiraz kabulü notu **tek başına değiştirmez** |
| F-6 | İmzalı, doğrulanabilir transkript PDF'i | ✅ | PDF + QR + herkese açık doğrulama sayfası |
| F-7 | Mezuniyet kontrolü, kalan dersler | ✅ | |
| F-8 | Raporlama | ✅ | başarı, doluluk, GANO dağılımı |
| **T-1** | Kayıt anı yarışı | ✅ | [docs/LOAD_TEST.md](docs/LOAD_TEST.md): k6 + veritabanı doğrulaması |
| **T-2** | Ön koşul DAG, çakışma, AKTS | ✅ | tüm ihlaller **tek yanıtta**; kapsam %98,8 + fuzz |
| **T-3** | Müfredat sürümleme, denklik | ✅ | zaman yolculuğu; yeni müfredat eski öğrenciyi etkilemez |
| **T-4** | Not motoru | ✅ | mutlak+bağıl (T-skoru), idempotent GANO işçisi, tekrar politikası |
| **T-5** | Program çözücü | ✅ | 60 ders/12 derslik/20 öğretim üyesi ms düzeyinde (hedef 30 sn) |
| **T-6** | Mezuniyet denetim motoru | ✅ | veri olarak kurallar; 400 rastgele örnekte kaba kuvvetle aynı sonuç |
| **T-7** | Yetki, denetim, imzalı belge | ✅ | çift onay, kilit, değiştirilemez denetim, HMAC QR |

## Kalite ve teslim kriterleri

| Kriter | Durum |
|---|---|
| Otomatik test (≥ 60) | **96 test fonksiyonu + 3 fuzz testi** (119 başarılı test, alt testler dahil); entegrasyon testleri **gerçek PostgreSQL**'e karşı |
| Fuzz / özellik testleri (≥ 2) | 3 fuzz (`FuzzNodeJSON`, `FuzzParseCode`, `FuzzVerifyPassword`) + 5 rastgele-girdili özellik testi |
| Çekirdek paket kapsamı ≥ %75 | `rules` 98,8 · `grading` 98,6 · `telemetry` 96,4 · `scheduler` 94,2 · `simulator` 89,7 · `graduation` 83,4 · `documents` 84,5 · `reports` 77,1 (çekirdek/alan paketleri; HTTP katmanı ağırlıklı paketlerin düşük kapsamı için bkz. § Yapılmayanlar) |
| CI (build + test + lint + `govulncheck` + imaj) | GitHub Actions; gofmt, vet, staticcheck, entegrasyon testleri, govulncheck, imaj ve frontend derlemesi |
| OpenAPI | 76 işlem, `/openapi.yaml`; **belge ile gerçek rotalar birebir eşleşir** (test) |
| Prometheus `/metrics` | HTTP istek/süre (yol deseniyle), eşzamanlı istek, kayıt sonuçları |
| OpenTelemetry izi | kayıt akışı, 7 span: [docs/TRACING.md](docs/TRACING.md) |
| k6 + SLO tablosu | [docs/LOAD_TEST.md](docs/LOAD_TEST.md): hedefler **tuttu**, sınırlamalar dürüstçe yazıldı |
| DECISIONS.md (≥ 7) | **27** karar kaydı |
| Kaos senaryosu (≥ 2) | **3** senaryo, gerçek sonuçlarla: [docs/CHAOS.md](docs/CHAOS.md) (2 gerçek hata bulundu ve düzeltildi) |
| RUNBOOK.md | [RUNBOOK.md](RUNBOOK.md): yedekleme/geri yükleme ve sıfır kesintili migrasyon **çalıştırılıp doğrulandı** |
| `EXPLAIN (ANALYZE)` | [docs/EXPLAIN.md](docs/EXPLAIN.md): 4 sorgu dizinli/dizinsiz; 2 dizin ölçümle **kaldırıldı** |
| Hızlı doğrulama yapısı | `make verify`, `TESTING.md`, `requests.http`, `make reset-demo`, kanıt komutları |

### Veritabanı tasarımı

Şema 12 migrasyonla kurulur. Bütünlük **veritabanına** bırakılmıştır: yabancı anahtarlar, `UNIQUE` (kısmi dizinlerle: bir öğrenci aynı şubede
tek aktif kayıt, sonuç başına tek bekleyen düzeltme), `CHECK` (kontenjan, not aralığı, roller), `NUMERIC` (para yok; notlar `NUMERIC(3,2)`,
`float` kullanılmadı), değiştirilemez tablolar için tetikleyiciler. Kritik sorgular için dizinler `EXPLAIN (ANALYZE)` ile **ölçülerek**
seçildi: aşağıda 3. soru ve [docs/EXPLAIN.md](docs/EXPLAIN.md).

## Yapılmayanlar ve bilinen sınırlamalar

Şartname "yapılmadı, nedeni şu" diyen dürüstlüğü, çalışıyormuş gibi sunmaktan üstün tutuyor. Eksiklerim:

**Yapılmayanlar**
- **Bonus maddelerin üçü de yapıldı** (bu başlıkta yalnızca sınırları belirtiliyor): danışman için **riskli öğrenci uyarısı yapıldı** (`GET /advisor/at-risk`: GANO < 2,00, dönem ortalaması düşüşü, son dönemde 2+ dersten kalma; devamsızlık verisi tutulmadığı için kullanılmadı). **İstek/sn grafiği yapıldı** (ana sayfa, `/metrics` sayacından 2 sn'de bir türetilir, son 2 dk). **Sürükle-bırak program editörü yapıldı** (Program & Sınav sayfası; `PATCH /section-meetings/{id}`: hedef hücre bırakmadan önce `dry_run` ile yeşil/kırmızı olur, çakışmalar nedenleriyle listelenir; dokunmatikte seç-ve-dokun). Editör yalnızca tek oturumlu şubeleri düzenler.
- **Arayüz için otomatik uçtan uca test** (Playwright) yazılmadı; arayüz için otomatik test yok; API davranışı `make verify` ile otomatik doğrulanıyor.
- **WAL arşivleme / PITR, yedek şifreleme, uzak yedek depolama** kurulu değil (RUNBOOK §4).
- **HTTP katmanı ağırlıklı paketlerin birim test kapsamı düşük**: `registration` %41, `auth` %38, `gradebook` %53, `timetable` %69, `cmd/api` %33 (şartnamenin %75 kuralı çekirdek/alan paketleri içindir ve onlar karşılanıyor). Bu katmanın davranışı `make verify` (19 kimlik) ve gerçek PostgreSQL'e karşı entegrasyon testleriyle uçtan uca doğrulanıyor, ama satır kapsamı olarak ölçülmüş değil.
- **Tek API örneği ve tek PostgreSQL**; yatay ölçekleme ve yüksek erişilebilirlik tasarlanmadı/denenmedi.

**Ölçülmemiş / kısıtlı ölçülmüş**
- **Yük testi 500 eşzamanlı sanal kullanıcıyla** yapıldı, 5.000 *ayrı* eşzamanlı istemciyle değil (k6, 7,6 GB Docker belleğinde 1.280. kullanıcıda
  bellek yetersizliğiyle öldürüldü). Toplam yük şartnameyle aynıdır (5.000 öğrenci, 15.000 istek). 5.000 eşzamanlı bağlantıda gecikmenin
  nasıl değişeceği **ölçülmedi** (docs/LOAD_TEST.md).
- **Ağ gecikmesi altında verim çöker**: 150 ms'de 1.335 → 13 istek/sn (kaos senaryosu B). Doğruluk korunur ama bir kayıt isteği ~13 ardışık
  veritabanı gidiş-dönüşü yapar. Bilinen iyileştirme: sorguları `pgx.Batch` ile gruplamak (yapılmadı).
- **Arayüz token'ları `localStorage`'dadır** (XSS'te okunabilir); üretimde `HttpOnly` çerez + CSRF korumasına geçilmelidir.

**Tasarım sınırları**
- Bekleme listesinde "adil sıra", **şube kilidini alma sırasıdır**; istemcinin isteği *gönderdiği* an değildir (ağ gecikmesi sırayı etkileyebilir).
- Mezuniyet çözücüsü durum sayısını 300 bin ile sınırlar (aşılırsa 422 `rules_too_complex`); en fazla 8 havuz.
- Program çözücüde aynı yarıyılın zorunlu dersleri tek grup sayılır; aynı dersin paralel şubeleri de birbirini dışlar.
- Belge iptali (revocation) ve `/verify` için hız sınırı yoktur; `DOC_SIGNING_KEY` değişirse eski belgeler doğrulanamaz (çift anahtar yok).

## Depo yapısı

```
backend/                Go API (cmd/api, cmd/simulator, cmd/verify) · internal/* · 12 migrasyon
frontend/               React + Vite + TypeScript + nginx
scripts/                k6 yük testi · chaos/ (3 kaos senaryosu) · runbook/ (yedekleme, sıfır kesintili migrasyon)
docs/                   LOAD_TEST · CHAOS · TRACING · EXPLAIN
DECISIONS.md  RUNBOOK.md  TESTING.md  requests.http  docker-compose.yml  Makefile  .github/workflows/ci.yml
```

---

## Doğrulama Soruları

### 1. En zorlandığın görev hangisiydi? Hangi alternatif çözümleri düşündün ve neden eledin?

**T-1 (kayıt anı yarışı)**; ikinci olarak **T-6** (mezuniyet atama problemi).

**T-1'de** iki şeyi birlikte sağlamak gerekiyordu: kontenjan **asla** aşılmamalı ve bekleme sırası **adil** olmalı. Düşündüklerim:

| Alternatif | Neden elendi |
|---|---|
| `SERIALIZABLE` + yeniden deneme | Aynı şubeye yüzlerce eşzamanlı istekte serileştirme hataları ve yeniden deneme fırtınası çıkarır; yine de sıra garantisi vermez |
| Yalnızca koşullu `UPDATE … WHERE enrolled_count < capacity` | Kontenjanı korur ama **adaleti değil**: dolu şubede `UPDATE` hiçbir satıra uymaz ve kilit tutmaz, bekleme listesine eklemeler sırasız olur |
| İyimser sürüm sütunu + yeniden deneme | Yeniden deneme fırtınası; adalet yok |
| `pg_advisory_xact_lock` | Satır kilidiyle aynı etkiyi verir ama veriye bağlı olmayan ek bir parça; satır kilidi doğal ve yeterli |
| Kuyruk (Redis/tek işçi) | Ek altyapı; sonuç eşzamansız olur (öğrenci anında "kayıtlı/bekleme #n" göremez), kuyruk ile veritabanı arasında tutarsızlık riski |

**Seçilen:** şube satırını `SELECT … FOR UPDATE` ile kilitlemek (aynı şubeye gelen herkes bu kilitte sıraya girer ve `enrollments.id`'ler
**kilit alma sırasıyla** verilir → sıra adil) + `CHECK (enrolled_count <= capacity)` **ikinci savunma hattı**. Kurallar (T-2) şube
kilidinden **önce** değerlendirilir ve aynı öğrencinin eşzamanlı kayıtları öğrenci satır kilidiyle sıralanır (AKTS limiti aşılamasın diye).

**T-6'da** bir dersin birden çok havuzda sayılabildiği atama problemi zordu: **açgözlü** yöntem yanlış sonuç verir (ortak dersi "dolu" havuza
koymak, o dersten başka çıkışı olmayan havuzu boşta bırakır; testte bu tuzak var); **akış (max-flow)** ders sayısı eşiğini çözer ama AKTS eşiğiyle
*birlikte*, dersi bölünemez saymayı ifade edemez. Seçilen: havuz ilerlemesini eşiğe kırparak izleyen **dinamik programlama**; 400 rastgele örnekte
tüm atamaları deneyen kaba kuvvetle **aynı en iyi sonucu** verir.

### 2. Çözümünün kırıldığı ya da yetersiz kaldığı bir senaryo (bilinen sınırlama)

**Ağ gecikmesi altında verim çöker** (kaos senaryosu B, ölçülmüş). API ile veritabanı arasına 150 ms (±50) gecikme konunca doğruluk korundu
(900/900 istek `201`, 5xx yok, kontenjan tutarlılığı PASS) ama **verim 1.335 → 13 istek/sn**'ye, p95 gecikme 57 ms → 4,3 sn'ye düştü.
Neden: bir kayıt isteği yaklaşık 13 **ardışık** veritabanı gidiş-dönüşü yapar (öğrenci kilidi, ~10 kural-verisi okuması, şube kilidi, ekleme,
denetim) ve her biri gecikmeyi öder; Little yasasına göre 40 eşzamanlı ÷ ~3 sn ≈ 13 istek/sn. Veritabanı uygulamayla aynı ağdaysa
(gidiş-dönüş < 1 ms) sorun değildir; farklı bölgedeyse kritiktir. İyileştirme (okumaları `pgx.Batch` ile gruplamak) bilinir ama **yapılmadı**.

İkinci sınırlama: **"adil sıra" kilit alma sırasıdır**, istemcinin isteği gönderdiği an değil; ağ gecikmesi daha önce gönderen birinin sırasını bozabilir.
Ayrıca yük testi 500 eşzamanlı sanal kullanıcıyla yapıldı (5.000 *ayrı* istemci ölçülmedi).

### 3. Eklediğin dizinlerden ikisi: hangi sorgu için, dizinsiz/dizinli fark nedir?

Gerçek sistemde (300 bin not, 12 bin öğrenci, 12 bin denetim kaydı) `EXPLAIN (ANALYZE, BUFFERS)` ile, dizini transaction içinde düşürüp `ROLLBACK` ile
geri alarak ölçüldü (tam çıktılar: [docs/EXPLAIN.md](docs/EXPLAIN.md)):

| Dizin | Sorgu | Dizinsiz | Dizinli | Fark |
|---|---|---:|---:|---|
| `enrollments_waitlist_idx (section_id, id) WHERE status='waitlisted'` | koltuk boşalınca bekleme listesinin ilk kişisi (`DropTx`, hot path) | 0,178 ms · 921 sayfa (`pkey` taraması + süzme) | **0,029 ms · 3 sayfa** (`Index Only Scan`) | **6× hızlı, 300× az sayfa** |
| `audit_log_entity_idx (entity, entity_id)` | "bu kaydı kim değiştirdi?" (T-7 denetim araması) | 0,98 ms · 347 sayfa (`Seq Scan`, 11.979 satır atılıyor) | **0,053 ms · 3 sayfa** | **19× hızlı, 115× az sayfa**; denetim tablosu yalnızca büyüdüğü için fark doğrusal artar |

**Ters örnek:** ilk tasarımda eklediğim **iki dizini ölçüm sonucu kaldırdım**: `course_results_term_idx` (dönem tablonun %20'si, düşük seçicilik:
dizinli 30,8 ms, dizinsiz paralel tarama **18,9 ms**, yani dizin daha yavaş) ve `course_results_student_idx` (`UNIQUE(student_id, …)` dizini
`student_id` ile başladığı için zaten aynı işi yapıyor: 0,223 vs 0,207 ms). Dizin eklemek bedelsiz değildir.

### 4. Yapay zekâ kullandıysan, düzelttiğin bir çıktıyı (önce/sonra) göster

Projeyi **Claude Code** ile geliştirdim. Üretilen kodun nasıl denetlendiğini gösteren somut düzeltmeler (hepsi testle ya da ölçümle yakalandı, görüşte değil):

**(a) Ders programı: öğretim üyesi müsaitliği sessizce kayboluyordu.** İlk (yapay zekâ üretimi) kod, öğretim üyelerini dilime ekleyip her birine
işaretçi tutuyordu:

```go
// ÖNCE (hatalı)
byUser := map[int64]*scheduler.Instructor{}
for _, id := range instructors {
    prob.Instructors = append(prob.Instructors, scheduler.Instructor{ID: "U" + id})
    byUser[id] = &prob.Instructors[len(prob.Instructors)-1]   // dilim büyüyünce ESKİ diziye işaret eder
}
// ... byUser[uid].Unavailable = append(...)                  // müsaitlik kaybolur
```

Dilime eleman eklendikçe yeniden tahsis olur ve önceki işaretçiler eski diziye bakar; müsaitlikler yanlış yere yazılıyordu. Sonuç: bütün hafta
müsait olmayan bir öğretim üyesine **yine de ders yerleştiriliyordu**. Entegrasyon testi (öğretim üyesini tüm hafta engelleyip `infeasible` bekleyen)
**kırıldı**. **Sonra:** müsaitlikler önce bir haritaya toplanır, öğretim üyeleri sonra oluşturulur (işaretçi yok).

**(b) Kaos testleri iki hata buldu.** Veritabanı kesintisinde API `500 internal` dönüyordu (804 istek) ve donmuş veritabanında `GET /me`
**`404 kullanıcı bulunamadı`** döndürüyordu:

```go
// ÖNCE: her veritabanı hatası "kullanıcı yok"
if err != nil { httpx.Error(w, 404, "not_found", "kullanıcı bulunamadı"); return }
// SONRA: yalnızca "kayıt yok" 404; altyapı hatası yeniden denenebilir 503 + Retry-After
if errors.Is(err, pgx.ErrNoRows) { httpx.Error(w, 404, ...); return }
if err != nil { httpx.Internal(w, err); return }   // IsTransient → 503
```

Neden yanlış: 500 "kodda hata var" demektir, istemciler yeniden denemez; 404 ise geçici bir altyapı sorununu kalıcı bir yanıta çevirir.
`internal/httpx/transient_test.go` ve `internal/auth/me_integration_test.go` bunu sabitler.

**(d) Aralıklı `make verify` hatası: imza kodunun kanonik olmayan yazımı.** `make verify` yaklaşık 16 koşuda bir `F-6: bozulmuş kod doğrulandı`
diyerek kırılıyordu. Bunu "kararsız test" diye geçmedim; kök nedeni buldum: imza 32 bayt = 256 bit, base64'te 43 karakter (258 bit), yani **son karakterde 2 kullanılmayan bit** var
ve Go'nun varsayılan çözücüsü bunları yok sayıyor. Aynı imzayı temsil eden **4 farklı kod** vardı; test son karakteri değiştirince, rastgele yine aynı imzaya denk gelebiliyordu.
Sahtecilik açığı değildi (imza değeri aynı), ama kodun tek yazımı olması gerekirdi.

```go
// ÖNCE: kullanılmayan bitleri yok sayar
sig, err = base64.RawURLEncoding.DecodeString(right)
// SONRA: yalnızca kanonik yazım
sig, err = base64.RawURLEncoding.Strict().DecodeString(right)
```

`TestParseCodeRejectsNonCanonicalSignature`, `Strict()` kaldırılınca "aynı imzayı temsil eden 4 farklı yazım kabul edildi" diye kırılıyor; düzeltmeden sonra 40 ardışık `make verify` koşusu hatasız geçti.

**(e) Dizin varsayımları ölçümle çürütüldü** (soru 3'teki ters örnek). Ayrıca CI ilk koşularda **kırıktı**: `go.mod` `go 1.26.0` yazdığı için
`setup-go` tam 1.26.0'ı kuruyor ve `govulncheck` o sürümün standart kütüphanesindeki bilinen zafiyetleri buluyordu; yerelde güncel yama sürümü
olduğu için görmemiştim. `go-version: "1.26"` + `check-latest` ile düzeltildi.

### 5. "Kayıt anı yarışı" çözümünün doğru çalıştığını nasıl kanıtladın?

Üç katmanla:

**1) Yük testi + veritabanı doğrulaması** (`make load`; ayrıntı ve SLO tablosu: [docs/LOAD_TEST.md](docs/LOAD_TEST.md)). 5.000 öğrenci × 3 şube
= 15.000 istek, kontenjanı 40 olan 30 şube (toplam 1.200 koltuk):

```
$ docker compose --profile load run --rm k6
  toplam istek                   15000
  verim (istek/sn)               1482.6
  hatalı istek oranı (5xx)       0.000 %
  gecikme p(95)                  426 ms        gecikme p(99)   495 ms
  kayıt (enrolled)               1200          bekleme (waitlisted)  13800
  eşik http_req_duration p(95)<5000: GEÇTİ   eşik http_req_failed rate<0.01: GEÇTİ

$ docker compose --profile sim run --rm simulator check
PASS: kontenjan aşımı yok; sayaçlar, sıra ve bekleme listesi tutarlı
```

`simulator check` altı SQL denetimi yapar: `enrolled_count > capacity` yok, gerçek `enrolled` satırı kontenjanı aşmıyor, sayaç gerçek satırlarla tutarlı,
aynı öğrencinin çift aktif kaydı yok, **sıra ihlali yok** (sonradan gelen koltuk almışken önce gelen bekliyor), **boş koltuk varken bekleyen yok**.

**2) Gerçek PostgreSQL'e karşı eşzamanlılık testi** (`internal/registration`): 500 öğrenci kontenjanı 40 olan şubeye **aynı anda** saldırır → tam 40
`enrolled`, 460 `waitlisted`, bekleme sırası tekrarsız, koltuk alanların hepsi bekleyenlerden önce gelmiş; 3 koltuk sahibi eşzamanlı bırakınca ilk 3
bekleyen sırayla otomatik atanır. Aynı öğrenci 10 şubeye eşzamanlı kaydolunca AKTS limiti (30) aşılmaz: tam 5 başarılı.

**3) Testin gerçekten yarışı yakaladığının kanıtı (mutasyon kontrolü).** Kilidi (`FOR UPDATE`) kodun bir kopyasında kaldırıp aynı testi çalıştırdım; test **kırıldı**:

```
--- FAIL: TestConcurrentEnrollNeverExceedsCapacity
  ... ERROR: new row for relation "sections" violates check constraint "sections_quota_ok" (SQLSTATE 23514)
```

Yani (a) test yarışı gerçekten yakalıyor, (b) kilit olmasa bile `CHECK` kısıtı aşımı **engelliyor** (istekler hata alır ama kontenjan aşılmaz): iki savunma hattı.
`make verify` ayrıca her koşuda 60 eşzamanlı istekle (kontenjan 10) aynı sonucu doğrular.

---

### Notlar

- **Yapay zekâ kullanımı:** şartname serbest bıraktığı için kod, test ve belgeler Claude Code ile üretildi; her çıktı testle, `make verify` ile ve
  ölçümle doğrulandı. Düzeltilen çıktılar 4. soruda.
- Geçmiş: her aşama ayrı, anlamlı mesajlı commit'tir (`git log --oneline`).
