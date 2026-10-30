# Dizin tasarımı: `EXPLAIN (ANALYZE)` ölçümleri

Şartname: *"Kritik sorgular için dizin tasarla ve `EXPLAIN (ANALYZE)` çıktısını README'ye ekle."*

**Yöntem.** Gerçek sistemde (PostgreSQL 16, simülasyon verisiyle: **300.182** not satırı, **12.575** öğrenci, **11.980** denetim
kaydı, **10.710** kayıt, 9.311'i bekleme listesinde). Her sorgu iki kez çalıştırıldı (ısınma + ölçüm). "Dizinsiz" ölçüm, dizini
bir transaction içinde `DROP INDEX` ile düşürüp `ROLLBACK` ile geri alarak yapıldı (PostgreSQL'de DDL işlemseldir, canlı
şema bozulmadı). Ölçüm betiği: bu belgedeki sorgular `psql` ile doğrudan çalıştırılabilir.

> Sürüm notu: bu belge 2026-10-30 ölçümlerini içerir. Süreler tek makine/sıcak önbellek içindir; **göreli farklar** anlamlıdır.

## Özet

| # | Sorgu | Dizin | Dizinli | Dizinsiz | Karar |
|---|---|---|---:|---:|---|
| 1 | Bekleme listesinin ilk kişisi (koltuk boşalınca terfi) | `enrollments_waitlist_idx` (kısmi) | **0,029 ms** · 3 sayfa | 0,178 ms · 921 sayfa | ✅ **6× hızlı**, 300× az sayfa → tutuldu |
| 4 | "Bu kaydı kim değiştirdi?" denetim araması | `audit_log_entity_idx` | **0,053 ms** · 3 sayfa | 0,98 ms · 347 sayfa | ✅ **19× hızlı**, 115× az sayfa → tutuldu |
| 2 | Dönem bazlı başarı raporu (F-8) | `course_results_term_idx` | 30,8 ms | **18,9 ms** | ❌ dizin **daha yavaş** → **kaldırıldı** (migrasyon 00012) |
| 3 | Öğrencinin tüm sonuçları (GANO) | `course_results_student_idx` | 0,223 ms | 0,207 ms | ❌ **gereksiz** (UNIQUE dizini aynı işi yapıyor) → **kaldırıldı** |

Dört dizinden ikisi ölçüm sonucu **kaldırıldı**. Bunlar benim ilk tasarımda "muhtemelen lazım olur" diye eklediğim dizinlerdi;
ölçüm bu varsayımı çürüttü. Dizin eklemek bedelsiz değildir: her yazmada bakım maliyeti ve disk kullanır.

---

## 1) Bekleme listesinin ilk kişisi — `enrollments_waitlist_idx`

Kullanım: `DropTx` bir koltuk boşalınca sıradaki öğrenciyi seçer (kilit altında, hot path).

```sql
SELECT id FROM enrollments WHERE section_id = 1001494 AND status = 'waitlisted' ORDER BY id LIMIT 1;
```

Dizin: `CREATE INDEX enrollments_waitlist_idx ON enrollments (section_id, id) WHERE status = 'waitlisted'` (kısmi: yalnızca bekleyenleri
indeksler; 408 kB).

**Dizinli** (`Index Only Scan`, 0 heap erişimi):
```
 Limit (actual time=0.013..0.013 rows=1 loops=1)
   ->  Index Only Scan using enrollments_waitlist_idx on enrollments (actual time=0.013..0.013 rows=1 loops=1)
         Index Cond: (section_id = 1001494)
         Heap Fetches: 0
         Buffers: shared hit=3
 Planning Time: 0.070 ms
 Execution Time: 0.029 ms
```

**Dizinsiz** (birincil anahtar sırasında tarayıp süzer: şubenin satırlarına ulaşana kadar başkalarını atlar):
```
 Limit (actual time=0.161..0.161 rows=1 loops=1)
   ->  Index Scan using enrollments_pkey on enrollments (actual time=0.161..0.161 rows=1 loops=1)
         Buffers: shared hit=921
 Planning Time: 0.068 ms
 Execution Time: 0.178 ms
```

**Fark:** 0,178 → 0,029 ms (**6×**); okunan sayfa 921 → 3 (**300×**). Dizinsiz plan tabloda geriye doğru yürüdüğü için maliyet,
şubenin kaç satırının *başkalarının arkasında* kaldığına bağlıdır: tablo büyüdükçe dizinsiz süre artar, dizinli süre sabit kalır.

## 4) Denetim araması — `audit_log_entity_idx`

Kullanım: "bu notu/kaydı kim, ne zaman değiştirdi?" (T-7 denetim sorgusu). Dizin: `(entity, entity_id)`.

```sql
SELECT at, actor_id, action FROM audit_log WHERE entity = 'enrollment' AND entity_id = '1000' ORDER BY id;
```

**Dizinli:**
```
   ->  Index Scan using audit_log_entity_idx on audit_log (actual time=0.016..0.017 rows=1 loops=1)
         Index Cond: ((entity = 'enrollment'::text) AND (entity_id = '1000'::text))
         Buffers: shared hit=3
 Execution Time: 0.053 ms
```

**Dizinsiz:**
```
   ->  Seq Scan on audit_log (actual time=0.099..0.947 rows=1 loops=1)
         Rows Removed by Filter: 11979
         Buffers: shared hit=347
 Execution Time: 0.980 ms
```

**Fark:** 0,98 → 0,053 ms (**19×**); 347 → 3 sayfa. Denetim tablosu yalnızca büyür (silinemez), bu yüzden dizinsiz tarama
**doğrusal** yavaşlar: 12 bin satırda 1 ms ise 10 milyon satırda ~1 sn olur; dizinli süre logaritmik kalır.

## 2) Dönem başarı raporu — `course_results_term_idx` (KALDIRILDI)

```sql
SELECT d.id, d.name, count(*), count(*) FILTER (WHERE cr.passed), round(avg(cr.grade_point)::numeric, 2)
  FROM course_results cr JOIN courses c ON c.id = cr.course_id JOIN departments d ON d.id = c.department_id
 WHERE cr.term_id = 1000007 AND NOT cr.exempt GROUP BY d.id, d.name ORDER BY d.id;
```

Seçili dönem **60.000 satır = tablonun %20'si**: bu seçicilik düşüktür.

**Dizinli:** `Bitmap Index Scan on course_results_term_idx` + `Bitmap Heap Scan` (60.000 satır) → **30,8 ms**
**Dizinsiz:** `Parallel Seq Scan on course_results` (3 işçi, 20.000 satır/işçi) → **18,9 ms**

**Sonuç:** Dizin **daha yavaş** (1,6×). Satırların beşte biri okunacaksa ardışık tarama, rastgele sayfa erişiminden ucuzdur.
Dizin kaldırıldı. Dönem sayısı artıp bir dönem tablonun küçük bir kesri olduğunda (kabaca <%5) yeniden ölçülmelidir.

## 3) Öğrencinin tüm sonuçları — `course_results_student_idx` (KALDIRILDI)

```sql
SELECT r.course_id, r.term_id, c.ects, r.grade_point::float8, r.exempt
  FROM course_results r JOIN courses c ON c.id = r.course_id WHERE r.student_id = 1000001;
```

**Dizinli:** `Index Scan using course_results_student_idx` → 0,223 ms
**Dizin düşürülünce:** `Index Scan using course_results_student_id_course_id_term_id_key` → **0,207 ms**

`UNIQUE (student_id, course_id, term_id)` kısıtının dizini `student_id` ile başladığı için `WHERE student_id = ?` sorgularına
zaten hizmet eder (en soldaki sütun öneki). Ayrı dizin **yinelenen**'di: aynı sürede, ek 2,2 MB ve her not eklemesinde ek yazma
maliyetiyle. Kaldırıldı.

---

## Dizin kullanım istatistiği (idx_scan)

`pg_stat_user_indexes`, tüm testler (doğrulama, yük, kaos) sonrası: tarama sayısı < 5 olan ve boyutça anlamlı dizinler:

| Dizin | Tarama | Boyut | Not |
|---|---:|---:|---|
| `course_results_student_id_course_id_term_id_key` | 2 | 12 MB | UNIQUE **kısıt**; asıl işi bütünlüğü zorlamak (taranmasa da gerekli) |
| `audit_log_actor_idx` | 0 | 520 kB | Mevcut hiçbir API ucu denetimi aktöre göre sorgulamıyor; adli soruşturma ("bu kullanıcı ne yaptı?") için **bilerek tutuluyor** |
| `enrollments_advisor_idx` | 1 | 344 kB | Danışman listesi; küçük veride sıralı tarama tercih ediliyor |

`UNIQUE`/`PRIMARY KEY` dizinleri kısıt gereği vardır; "tarama 0" onları gereksiz kılmaz. `audit_log_actor_idx` ise **ölçümle
değil, amaçla** tutulan tek dizindir: şu an hiçbir özellik kullanmıyor, bu bilinçli bir risk kararıdır.

## Sınırlamalar

- Tek makine, sıcak önbellek; soğuk önbellekte (diskten okuma) dizin farkı **daha büyük** olur.
- Veri 12 bin öğrenci ölçeğindedir. Dizinsiz sorgular, tablo büyüdükçe doğrusal yavaşlar; farkın büyüyeceği öngörüsü ölçülmüş
  değil, planlardan çıkarılmıştır (sıralı tarama vs. dizin erişimi).
- Yalnızca 4 sorgu ölçüldü; tüm uç noktaların planları incelenmedi.
