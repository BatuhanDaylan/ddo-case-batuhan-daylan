#!/bin/sh
# Yedekleme + geri yükleme provası. Canlı veritabanına DOKUNMAZ: yedeği ayrı, geçici bir veritabanına yükler,
# özdeş olduğunu doğrular ve geçici veritabanını siler.
#
#   sh scripts/runbook/backup-restore.sh            # yedek ./backups/ altına yazılır
set -eu
export MSYS_NO_PATHCONV=1   # Windows Git Bash yol dönüşümünü kapatır (Linux/macOS’ta etkisiz)
cd "$(dirname "$0")/../.."
set -a; . ./.env; set +a
STAMP=$(date +%Y%m%d-%H%M%S)
DIR="${BACKUP_DIR:-backups}"
FILE="$DIR/uninet-$STAMP.dump"
SCRATCH="restore_check_$$"
mkdir -p "$DIR"

psql_db() { docker compose exec -T db psql -U "$POSTGRES_USER" -d "$1" -At -c "$2"; }

echo "1) Yedek alınıyor (pg_dump, özel biçim, sıkıştırılmış) → $FILE"
# --no-owner/--no-acl: yedek başka bir kullanıcı/ortamda da geri yüklenebilsin.
T0=$(date +%s)
docker compose exec -T db pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc --no-owner --no-acl > "$FILE"
echo "   boyut: $(wc -c < "$FILE") bayt, süre: $(( $(date +%s) - T0 )) sn"

echo "2) Geçici veritabanına geri yükleniyor ($SCRATCH) — canlı veritabanı etkilenmez"
docker compose exec -T db psql -U "$POSTGRES_USER" -d postgres -c "CREATE DATABASE $SCRATCH" >/dev/null
trap 'docker compose exec -T db psql -U "$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS '"$SCRATCH"' WITH (FORCE)" >/dev/null 2>&1 || true' EXIT
T0=$(date +%s)
docker compose exec -T db pg_restore -U "$POSTGRES_USER" -d "$SCRATCH" --no-owner --no-acl --exit-on-error < "$FILE"
echo "   geri yükleme süresi: $(( $(date +%s) - T0 )) sn"

echo "3) Doğrulama: tablo satır sayıları ve içerik özeti (canlı ↔ geri yüklenen)"
# Not: n_live_tup yaklaşıktır; kesin sonuç için count(*) kullanılır.
TABLES=$(psql_db "$POSTGRES_DB" "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1")
FAIL=0; N=0
for t in $TABLES; do
  a=$(psql_db "$POSTGRES_DB" "SELECT count(*) FROM \"$t\"")
  b=$(psql_db "$SCRATCH" "SELECT count(*) FROM \"$t\"")
  N=$((N+1))
  if [ "$a" != "$b" ]; then echo "   FAIL $t: canlı=$a geri_yüklenen=$b"; FAIL=1; fi
done
echo "   $N tablonun satır sayısı karşılaştırıldı"

# İçerik özeti: denetim kaydı ve notlar bozulmadan geldi mi?
SUM_Q="SELECT md5(string_agg(id::text || ':' || action || ':' || entity, ',' ORDER BY id)) FROM audit_log"
a=$(psql_db "$POSTGRES_DB" "$SUM_Q"); b=$(psql_db "$SCRATCH" "$SUM_Q")
[ "$a" = "$b" ] && echo "   audit_log içerik özeti özdeş ($a)" || { echo "   FAIL audit_log özeti: $a ≠ $b"; FAIL=1; }
SUM_Q="SELECT md5(string_agg(id::text || ':' || letter, ',' ORDER BY id)) FROM course_results"
a=$(psql_db "$POSTGRES_DB" "$SUM_Q"); b=$(psql_db "$SCRATCH" "$SUM_Q")
[ "$a" = "$b" ] && echo "   course_results içerik özeti özdeş ($a)" || { echo "   FAIL course_results özeti: $a ≠ $b"; FAIL=1; }

# Kısıtlar ve tetikleyiciler yedekle birlikte geldi mi? (değiştirilemez denetim kaydı güvencesi)
TRG=$(psql_db "$SCRATCH" "SELECT count(*) FROM pg_trigger WHERE tgname LIKE 'audit_log_no_%'")
[ "$TRG" -ge 2 ] && echo "   audit_log değiştirilemezlik tetikleyicileri geri yüklendi ($TRG)" || { echo "   FAIL tetikleyiciler eksik"; FAIL=1; }
if docker compose exec -T db psql -U "$POSTGRES_USER" -d "$SCRATCH" -c "UPDATE audit_log SET action='x'" >/dev/null 2>&1; then
  echo "   FAIL geri yüklenen veritabanında audit_log değiştirilebiliyor"; FAIL=1
else
  echo "   geri yüklenen veritabanında audit_log UPDATE'i reddediliyor"
fi

if [ "$FAIL" = 0 ]; then echo "SONUÇ: PASS — yedek geçerli ve geri yüklenebilir"; else echo "SONUÇ: FAIL"; exit 1; fi
