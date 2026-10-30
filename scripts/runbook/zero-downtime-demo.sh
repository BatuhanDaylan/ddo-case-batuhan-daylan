#!/bin/sh
# Sıfır kesintili migrasyon provası: expand → migrate → contract.
#
# Örnek değişiklik: people.full_name sütununu people.display_name olarak yeniden adlandırmak.
# Doğrudan `ALTER TABLE ... RENAME COLUMN` eski uygulama sürümünü ANINDA bozar. Bunun yerine üç aşamada yapılır;
# her aşamada hem ESKİ (v1) hem YENİ (v2) uygulama sürümü hatasız yazabilir:
#
#   1) EXPAND   — yeni sütunu ekle (yalnızca metadata, anlık) + iki yönlü eşitleme tetikleyicisi
#   2) MIGRATE  — mevcut satırları küçük gruplar halinde doldur (kısa kilitler); v2'yi dağıt (v1 ile birlikte çalışır)
#   3) CONTRACT — v1 tamamen kapandıktan sonra tetikleyiciyi ve eski sütunu kaldır
#
# Canlı şemaya DOKUNMAZ: geçici bir veritabanında çalışır. Yük, pgbench ile üretilen gerçek eşzamanlı yazmalardır.
set -eu
export MSYS_NO_PATHCONV=1   # Windows Git Bash '/tmp/x.sql' yolunu Windows yoluna çevirip bozmasın (Linux/macOS'ta etkisiz)
cd "$(dirname "$0")/../.."
set -a; . ./.env; set +a
DB="zd_demo_$$"
PSQL="docker compose exec -T db psql -U $POSTGRES_USER -v ON_ERROR_STOP=1 -q"
q() { $PSQL -d "$DB" -At -c "$1"; }
ms() { date +%s%3N; }

cleanup() { $PSQL -d postgres -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

echo "Hazırlık: geçici veritabanı $DB, 50.000 satırlık tablo"
$PSQL -d postgres -c "CREATE DATABASE $DB" >/dev/null
$PSQL -d "$DB" >/dev/null <<'SQL'
CREATE TABLE people (id bigserial PRIMARY KEY, full_name text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
INSERT INTO people (full_name) SELECT 'eski-' || g FROM generate_series(1, 50000) g;
SQL
INITIAL=$(q "SELECT count(*) FROM people")

# v1 (eski uygulama) yalnızca full_name yazar; v2 (yeni uygulama) yalnızca display_name yazar.
docker compose exec -T db sh -c "echo \"INSERT INTO people (full_name) VALUES ('v1-' || random()::text);\" > /tmp/v1.sql"
docker compose exec -T db sh -c "echo \"INSERT INTO people (display_name) VALUES ('v2-' || random()::text);\" > /tmp/v2.sql"
bench() { docker compose exec -T db pgbench -U "$POSTGRES_USER" -d "$DB" -n -c 4 -j 2 -T "$2" -f "$1" 2>&1; }

OUT1="${TMPDIR:-/tmp}/zd-v1.txt"; OUT2="${TMPDIR:-/tmp}/zd-v2.txt"
echo "t=0 s   v1 (eski uygulama) yazmaya başlıyor (16 sn, 4 eşzamanlı)"
bench /tmp/v1.sql 16 > "$OUT1" &
P1=$!
sleep 2

echo "t=2 s   AŞAMA 1 — EXPAND (kilit beklemesi en fazla 2 sn; uzun işlemin ardında kuyruk oluşturmaz)"
T0=$(ms)
$PSQL -d "$DB" >/dev/null <<'SQL'
SET lock_timeout = '2s';
ALTER TABLE people ADD COLUMN display_name text;       -- metadata-only: tabloyu yeniden yazmaz
CREATE FUNCTION people_sync() RETURNS trigger AS $$
BEGIN
  IF NEW.display_name IS NULL THEN NEW.display_name := NEW.full_name; END IF;  -- v1 yazdı → yeni sütunu doldur
  IF NEW.full_name    IS NULL THEN NEW.full_name    := NEW.display_name; END IF; -- v2 yazdı → eski sütunu doldur
  RETURN NEW;
END $$ LANGUAGE plpgsql;
-- full_name NOT NULL kısıtı BEFORE tetikleyiciden SONRA denetlenir, bu yüzden v2'nin eksik full_name'i sorun olmaz.
CREATE TRIGGER people_sync BEFORE INSERT OR UPDATE ON people FOR EACH ROW EXECUTE FUNCTION people_sync();
SQL
echo "        expand süresi: $(( $(ms) - T0 )) ms"

echo "t=3 s   AŞAMA 2 — MIGRATE: 5.000'lik gruplarla geriye dönük doldurma (v1 yazmaya devam ediyor)"
MAXB=0; BATCHES=0
while :; do
  B0=$(ms)
  n=$(q "WITH b AS (SELECT id FROM people WHERE display_name IS NULL ORDER BY id LIMIT 5000 FOR UPDATE SKIP LOCKED),
              u AS (UPDATE people p SET display_name = p.full_name FROM b WHERE p.id = b.id RETURNING 1)
         SELECT count(*) FROM u")
  D=$(( $(ms) - B0 )); [ "$D" -gt "$MAXB" ] && MAXB=$D
  BATCHES=$((BATCHES+1))
  [ "$n" -eq 0 ] && break
  sleep 0.05 # çoğaltma/denetim yükü için nefes payı
done
echo "        $BATCHES grup, en uzun tek grup: ${MAXB} ms (her grup kısa bir işlem; tabloyu uzun süre kilitlemez)"

echo "t=5 s   v2 (yeni uygulama) dağıtılıyor: v1 ile BİRLİKTE yazıyor (rolling deploy), 14 sn"
bench /tmp/v2.sql 14 > "$OUT2" &
P2=$!

wait "$P1" || true
grep -q 'number of transactions actually processed' "$OUT1" || { echo "FAIL: v1 yük aracı çalışmadı:"; cat "$OUT1"; exit 1; }
echo "t=18 s  v1 tamamen kapandı (boşaltıldı)"
NULLS=$(q "SELECT count(*) FROM people WHERE display_name IS NULL")
echo "        display_name boş satır sayısı: $NULLS (0 olmalı: contract'a geçilebilir)"
[ "$NULLS" = 0 ] || { echo "FAIL: backfill eksik"; exit 1; }

echo "t=19 s  AŞAMA 3 — CONTRACT (v2 hâlâ yazıyor)"
T0=$(ms)
$PSQL -d "$DB" >/dev/null <<'SQL'
SET lock_timeout = '2s';
DROP TRIGGER people_sync ON people;
DROP FUNCTION people_sync();
-- NOT NULL'u doğrudan eklemek tüm tabloyu tarar ve kilitler; önce NOT VALID kısıt, sonra ayrı doğrulama (kısa kilit):
ALTER TABLE people ADD CONSTRAINT people_display_name_nn CHECK (display_name IS NOT NULL) NOT VALID;
ALTER TABLE people VALIDATE CONSTRAINT people_display_name_nn;   -- yazmaları engellemeden tarar
ALTER TABLE people ALTER COLUMN display_name SET NOT NULL;       -- doğrulanmış kısıt sayesinde tarama gerekmez
ALTER TABLE people DROP CONSTRAINT people_display_name_nn;
ALTER TABLE people DROP COLUMN full_name;                        -- v2 bu sütunu hiç kullanmıyor
SQL
echo "        contract süresi: $(( $(ms) - T0 )) ms"
wait "$P2" || true
grep -q 'number of transactions actually processed' "$OUT2" || { echo "FAIL: v2 yük aracı çalışmadı:"; cat "$OUT2"; exit 1; }

echo
echo "=== Sonuçlar ==="
for f in "$OUT1" "$OUT2"; do
  name=$([ "$f" = "$OUT1" ] && echo "v1 (eski)" || echo "v2 (yeni)")
  proc=$(sed -n 's/^number of transactions actually processed: \([0-9]*\).*/\1/p' "$f")
  fail=$(sed -n 's/^number of failed transactions: \([0-9]*\).*/\1/p' "$f")
  lat=$(sed -n 's/^latency average = \(.*\)/\1/p' "$f")
  echo "$name: işlenen=$proc  BAŞARISIZ=${fail:-0}  ortalama gecikme=$lat"
  if [ "${fail:-0}" != 0 ]; then echo "FAIL: $name yazmaları hata aldı"; exit 1; fi
done
P1N=$(sed -n 's/^number of transactions actually processed: \([0-9]*\).*/\1/p' "$OUT1")
P2N=$(sed -n 's/^number of transactions actually processed: \([0-9]*\).*/\1/p' "$OUT2")
TOTAL=$(q "SELECT count(*) FROM people")
EXPECT=$(( INITIAL + P1N + P2N ))
echo "satır sayısı: $TOTAL (beklenen: başlangıç $INITIAL + v1 $P1N + v2 $P2N = $EXPECT)"
[ "$TOTAL" = "$EXPECT" ] || { echo "FAIL: veri kaybı"; exit 1; }
echo "son şema: $(q "SELECT string_agg(column_name || ' ' || data_type || CASE WHEN is_nullable='NO' THEN ' NOT NULL' ELSE '' END, ', ' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='people'")"
echo "SONUÇ: PASS — üç aşamada da hiçbir yazma başarısız olmadı, veri kaybı yok"
