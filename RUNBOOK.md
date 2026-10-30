# RUNBOOK — UniNet işletim kılavuzu

Bu belgedeki prosedürlerin **yedekleme/geri yükleme** ve **sıfır kesintili migrasyon** bölümleri gerçek sistemde çalıştırılıp
doğrulanmıştır (betikler `scripts/runbook/`). Sonuçlar ilgili bölümlerdedir.

## 1. Servisler

| Servis | Port | Rol | Sağlık denetimi |
|---|---|---|---|
| `frontend` | 3000 | nginx (root olmayan); arayüz + `/api` → backend | `GET /` |
| `backend` | 8080 | Go API; açılışta migrasyon + (isteğe bağlı) seed; GANO işçisi | `GET /health` (veritabanı dahil) |
| `db` | — (yalnızca compose ağı) | PostgreSQL 16; `pgdata` volume'ü | `pg_isready` |

```bash
docker compose up --build -d     # başlat
docker compose ps                # durum (hepsi healthy olmalı)
docker compose logs -f backend   # JSON günlük
docker compose down              # durdur (veri korunur)
make reset-demo                  # DİKKAT: volume'ü siler, ilk seed haline döner
```

Backend yalnızca `db` **healthy** olunca başlar (`depends_on: service_healthy`); soğuk makinede (boş Docker önbelleği)
5 dakika içinde hazır olacak şekilde tasarlanmıştır.

## 2. Yapılandırma (ortam değişkenleri)

| Değişken | Anlamı | Not |
|---|---|---|
| `DATABASE_URL` | PostgreSQL bağlantısı | compose `.env`'den kurar |
| `JWT_SECRET` | Access token imzası (≥32 karakter) | değişirse **tüm access token'lar geçersiz olur** (refresh ile yenilenir) |
| `DOC_SIGNING_KEY` | Belge (transkript) HMAC anahtarı (≥32 karakter) | **değişirse daha önce verilmiş tüm belgeler DOĞRULANAMAZ olur.** JWT sırrından ayrıdır |
| `PUBLIC_BASE_URL` | QR kodun işaret ettiği adres | belgelere basılır; sonradan değiştirmek eski QR'ların adresini etkiler |
| `DB_MAX_CONNS` | Veritabanı havuzu (varsayılan 40) | PostgreSQL `max_connections` (100) altında kalmalı |
| `HASH_CONCURRENCY` | Eşzamanlı argon2 doğrulaması (varsayılan 4) | her biri ~64 MiB bellek kullanır |
| `SEED_DEMO` | Demo hesap/veri | **üretimde `false`** olmalı |
| `OTEL_TRACES_EXPORTER` | `stdout` ile izleme açılır | bkz. docs/TRACING.md |

`.env` repoya girmez; `.env.example` yalnızca geliştirme değerleri içerir. Üretimde tüm sırlar değiştirilmelidir.

## 3. Gözlemlenebilirlik

- **Metrikler:** `GET /metrics` (Prometheus). Başlıca seriler: `uninet_http_requests_total{method,route,status}`,
  `uninet_http_request_duration_seconds`, `uninet_http_requests_in_flight`, `uninet_enrollment_attempts_total{outcome}`.
  Etiketler yol *desenidir*, kimlik değil (kardinalite sınırlı). `/metrics` üretimde ağ düzeyinde kısıtlanmalıdır.
- **Günlük:** yapılandırılmış JSON (`slog`); parola/token asla yazılmaz.
- **İzleme:** docs/TRACING.md.
- **Önerilen uyarılar:** `5xx` oranı > %1 (5 dk); `uninet_http_requests_in_flight` sürekli yüksek; `/health` 503;
  `gano_jobs` tablosunda en eski satır > 5 dk (işçi takıldı).

## 4. Yedekleme ve geri yükleme

### Yedek alma

```bash
sh scripts/runbook/backup-restore.sh   # alır + ayrı veritabanına geri yükleyip DOĞRULAR (canlıya dokunmaz)
```

Elle yalnızca yedek almak için:

```bash
docker compose exec -T db pg_dump -U $POSTGRES_USER -d $POSTGRES_DB -Fc --no-owner --no-acl > uninet-$(date +%F).dump
```

`-Fc` özel biçimdir (sıkıştırılmış, seçmeli geri yüklemeye uygun). Yedekler `backups/` altına yazılır (repoya girmez).

### Geri yükleme

```bash
# 1) Uygulamayı durdur (yazma olmasın)
docker compose stop backend
# 2) Hedef veritabanını sıfırla ve yükle
docker compose exec -T db psql -U $POSTGRES_USER -d postgres -c "DROP DATABASE $POSTGRES_DB WITH (FORCE)" -c "CREATE DATABASE $POSTGRES_DB"
docker compose exec -T db pg_restore -U $POSTGRES_USER -d $POSTGRES_DB --no-owner --no-acl --exit-on-error < uninet-YYYY-MM-DD.dump
# 3) Başlat ve doğrula
docker compose start backend
make verify
```

Yedek, şema **ve** değiştirilemezlik tetikleyicilerini (`audit_log`, `grade_history`, `documents`) birlikte taşır; geri yüklenen
veritabanında bu tablolar yine değiştirilemezdir.

### Doğrulanmış sonuç (2026-10-30, 12.000 öğrencilik veri, ~3,3 MB yedek)

| Adım | Süre / sonuç |
|---|---|
| `pg_dump` | 1 sn |
| Geçici veritabanına `pg_restore` | 3 sn |
| 30 tablonun satır sayısı (canlı ↔ geri yüklenen) | **hepsi özdeş** |
| `audit_log` ve `course_results` içerik özeti (md5) | **özdeş** |
| Değiştirilemezlik tetikleyicileri | geri yüklendi; `UPDATE audit_log` reddediliyor |

### Sınırlamalar

- Yedek **anlık görüntüdür** (RPO = son yedekten bu yana geçen süre). Zamana göre geri dönüş (PITR) için WAL arşivleme
  (`archive_mode`, `pg_basebackup`) kurulmalıdır; bu repoda **kurulu değildir**.
- Yedekler şifrelenmez ve uzak depolamaya gönderilmez; üretimde ikisi de gereklidir.
- Geri yükleme provası düzenli (ör. aylık) tekrarlanmalıdır: denenmemiş yedek, yedek değildir.

## 5. Sıfır kesintili migrasyon (expand → migrate → contract)

Migrasyonlar `backend/internal/db/migrations/*.sql` (goose) dosyalarıdır ve API açılışında otomatik uygulanır. Kural: **her
migrasyon, çalışan bir önceki uygulama sürümüyle uyumlu olmalıdır** (rolling deploy sırasında eski ve yeni örnek birlikte
çalışır). Bu yüzden kırıcı değişiklikler (sütun yeniden adlandırma, tür değiştirme, `NOT NULL` ekleme) üç aşamada yapılır:

| Aşama | Ne yapılır | Uygulama durumu |
|---|---|---|
| **1. Expand** | Yeni yapıyı **ekle** (sütun, tablo, dizin); eski ve yeni arasında eşitleme (tetikleyici ya da çift yazma). Eski yapıya dokunma | v1 çalışıyor |
| **2. Migrate** | Mevcut veriyi **küçük gruplarla** taşı; v2'yi dağıt (v1 ile birlikte çalışır) | v1 + v2 birlikte |
| **3. Contract** | v1 tamamen kapandıktan **sonra** eski yapıyı **kaldır** | yalnızca v2 |

Örnek: `people.full_name` → `people.display_name` yeniden adlandırması. `RENAME COLUMN` doğrudan çalıştırılsa v1 anında
bozulurdu. Çalıştırılabilir ve doğrulanmış provası:

```bash
sh scripts/runbook/zero-downtime-demo.sh   # geçici veritabanı; pgbench ile gerçek eşzamanlı yazma yükü altında
```

Betiğin uyguladığı adımlar ve alınan önlemler:

1. **Expand:** `ADD COLUMN display_name text` (yalnızca metadata, tabloyu yeniden yazmaz) + iki yönlü eşitleme tetikleyicisi
   (v1 `full_name` yazınca `display_name` dolar; v2 `display_name` yazınca `full_name` dolar). `SET lock_timeout='2s'`: kilit
   alınamazsa DDL **hızlıca hata verir**, uzun bir işlemin ardında kuyruk oluşturup tüm yazmaları engellemez.
2. **Migrate:** 5.000'lik gruplarla geriye dönük doldurma (`FOR UPDATE SKIP LOCKED`); her grup kısa bir işlemdir.
   v2, v1 çalışırken dağıtılır.
3. **Contract:** v1 boşaltıldıktan sonra tetikleyici ve eski sütun kaldırılır. `NOT NULL` doğrudan eklenmez (tüm tabloyu tarar
   ve kilitler): önce `CHECK … NOT VALID`, ardından ayrı `VALIDATE CONSTRAINT` (yazmaları engellemez), sonra `SET NOT NULL`.

**Doğrulanmış sonuç** (50.000 satırlık tablo; v1 ve v2 her biri 4 eşzamanlı yazıcı):

| Ölçüm | Değer |
|---|---|
| v1 (eski uygulama) yazmaları | 9.376 işlendi, **0 başarısız** |
| v2 (yeni uygulama) yazmaları | 5.488 işlendi, **0 başarısız** |
| Veri kaybı | **yok** (64.864 satır = 50.000 + 9.376 + 5.488) |
| En uzun tek kilit/grup | expand 313 ms · en uzun backfill grubu 338 ms · contract 342 ms |
| Son şema | `display_name text NOT NULL` (`full_name` kaldırıldı) |

### Geri alma

- **Expand/Migrate aşamasında** sorun çıkarsa v2'yi geri çek; eski yapı bozulmadığı için v1 çalışmaya devam eder (tetikleyici
  v1'in yazdıklarını yeni sütuna taşımayı sürdürür).
- **Contract aşamasından sonra** geri dönüş yoktur (sütun silindi): bu yüzden contract yalnızca v1'in **tamamen** kapandığı ve
  backfill'in eksiksiz olduğu (`display_name IS NULL` sayısı 0) doğrulandıktan sonra, **ayrı bir dağıtımda** yapılır.
- Her kırıcı değişiklik öncesi §4'teki yedek alınır.

### Bu repodaki migrasyonlar

Mevcut 11 migrasyon yalnızca **ekleyicidir** (yeni tablo/sütun/dizin); mevcut yapıyı kıran bir değişiklik yoktur, bu yüzden
tek başına expand aşamasına denk gelirler. Yeni sütunlar `DEFAULT`'lu ya da `NULL`'dır, böylece eski sürüm onları yazmadan da çalışır.

## 6. Olay müdahalesi

| Belirti | Olası neden | Ne yapılır |
|---|---|---|
| Çok sayıda `503 service_unavailable` | Veritabanı kesintisi/yeniden başlatma | `docker compose ps db`; `docker compose logs db`. API kendiliğinden toparlanır (backend yeniden başlatılmaz). İstemciler `Retry-After` ile yeniden dener; kayıt isteklerinde **aynı `Idempotency-Key`** kullanılmalı. Kanıt: docs/CHAOS.md |
| `/health` 503 | Veritabanına ulaşılamıyor / donmuş | DB sürecini ve diskini denetle; donmuşsa istekler 25 sn'de zaman aşımına uğrar (askıda kalmaz) |
| Kayıtlı öğrenci yok ama bekleme listesi dolu | — | `make verify` + `docker compose --profile sim run --rm simulator check` tutarsızlık olup olmadığını söyler; kontenjan korunması veritabanı kısıtına (`sections_quota_ok`) dayanır |
| GANO güncel değil | İşçi kuyruğu birikti | `SELECT count(*), min(enqueued_at) FROM gano_jobs;` işçi kuyruğu her saniye boşaltır; süreç yeniden başlatılırsa kalan işler sürer (iş idempotenttir) |
| Belge doğrulanamıyor (eski belgeler) | `DOC_SIGNING_KEY` değişti | Anahtarı eski değerine döndür; anahtar rotasyonu planlanıyorsa çift anahtarlı doğrulama gerekir (**yok**) |
| Giriş yavaş / 503 | argon2 sınırı (`HASH_CONCURRENCY`) dolu | Eşzamanlı giriş yükünü azalt ya da sınırı/CPU'yu artır; bellek ≈ sınır × 64 MiB |
| Not geçmişi/denetim değiştirilemiyor | Beklenen davranış | Tetikleyici bilerek engeller; düzeltme yeni bir kayıt olarak eklenir |

## 7. Bilinen sınırlamalar (işletim açısından)

- Tek API örneği ve tek PostgreSQL (replika yok); yüksek erişilebilirlik tasarlanmadı.
- WAL arşivleme/PITR, yedek şifreleme ve uzak yedek depolama kurulu değil (§4).
- Sır yönetimi `.env` dosyasıdır; üretimde bir sır yöneticisi kullanılmalıdır.
- `/metrics` ve `/openapi.yaml` kimlik doğrulamasızdır; üretimde ağ düzeyinde kısıtlanmalıdır.
- Otomatik ölçekleme, yük dengeleyici ve TLS sonlandırma kapsam dışıdır (yerel Docker Compose hedeflenmiştir).
