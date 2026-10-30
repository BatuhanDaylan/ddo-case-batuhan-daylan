#!/bin/sh
# Kaos senaryosu A: YÜK ALTINDA veritabanı yeniden başlatma.
#
# Hipotez: PostgreSQL kayıt anı yükü sürerken yeniden başlatılırsa
#   (1) API süreci ÇÖKMEZ ve yeniden başlatılmaya gerek kalmadan toparlanır (havuz yeniden bağlanır),
#   (2) kesinti süresince istekler hızlıca hata (5xx) döner, askıda kalmaz,
#   (3) hiçbir veri bozulmaz: kontenjan aşımı yok, sayaçlar/sıra tutarlı (yarım kalan transaction'lar geri alınır),
#   (4) kesinti sonrası sistem tam işlevseldir (make verify PASS).
. "$(dirname "$0")/lib.sh"

title "A) Yük altında veritabanı yeniden başlatma"
need_stack
seed_data
BEFORE=$(backend_started_at)
LOG="${TMPDIR:-/tmp}/chaos-a-probe.log"
OUT="${TMPDIR:-/tmp}/chaos-a-storm.txt"

start_probe "$LOG"
say "yük başlıyor: 3000 öğrenci × 3 şube, 60 eşzamanlı (arka planda)"
docker compose --profile sim run --rm -T simulator storm -students 3000 -per-student 3 -concurrency 60 -offset 5000 > "$OUT" 2>&1 &
STORM=$!
sleep 3

say "veritabanı yeniden başlatılıyor (docker compose restart db) …"
T0=$(now_ms)
docker compose restart db >/dev/null 2>&1
say "yeniden başlatma komutu $(( $(now_ms) - T0 )) ms sürdü"
RECOVERY=$(wait_health 60) || true
say "API /health tekrar 200 olana kadar geçen süre (komut dönüşünden): ${RECOVERY} ms"

wait "$STORM" || true
sleep 1
stop_probe

title "Sonuçlar"
summarize_probe "$LOG"
say "yük çıktısı:"
sed 's/^/    /' "$OUT"

AFTER=$(backend_started_at)
if [ "$BEFORE" = "$AFTER" ]; then say "backend süreci YENİDEN BAŞLAMADI (kendi kendine toparlandı): PASS"; else say "backend yeniden başlamış: FAIL"; fi

final_checks
