#!/bin/sh
# Kaos senaryosu C: veritabanı DONMASI (süreç çalışıyor ama yanıt vermiyor; docker pause).
#
# "Yeniden başlatma" bağlantıyı hızlıca koparır; DONMA daha sinsidir: bağlantı açık kalır, hiçbir şey dönmez.
# İki aşama:
#   1) KISA donma (8 sn): veritabanı gerektiren istek donmadan sağ çıkmalı (çözülünce tamamlanır),
#      /health ise askıda kalmadan ~2 sn içinde 503 vermeli.
#   2) UZUN donma (30 sn): istek sonsuza dek ASKIDA KALMAMALI; istek zaman aşımında (25 sn) 503 ile bitmeli.
# Her iki aşamadan sonra sistem kendiliğinden toparlanmalı (backend yeniden başlamadan) ve tam işlevsel olmalı.
. "$(dirname "$0")/lib.sh"

title "C) Veritabanı donması (docker pause)"
need_stack
BEFORE=$(backend_started_at)
DB=$(docker compose ps -q db)

# Donmadan ÖNCE token al (giriş veritabanı ister).
TOKEN=$(curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"ogrenci@uninet.test","password":"Demo1234!"}' | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$TOKEN" ] || { echo "token alınamadı" >&2; exit 2; }
ME() { curl -s -o /dev/null -w '%{http_code} %{time_total}' --max-time 45 "$API/me" -H "Authorization: Bearer $TOKEN" || true; }

LOG="${TMPDIR:-/tmp}/chaos-c-probe.log"
start_probe "$LOG"
sleep 1

title "Aşama 1 — kısa donma (8 sn)"
docker pause "$DB" >/dev/null
ME > "${TMPDIR:-/tmp}/chaos-c-me1.txt" &
ME_PID=$!
sleep 1
say "donukken GET /health → HTTP/süre: $(curl -s -o /dev/null -w '%{http_code} %{time_total}' --max-time 10 "$API/health" || true) sn"
sleep 7
docker unpause "$DB" >/dev/null
wait "$ME_PID" || true
say "donma sırasında başlayan GET /me (donma bitince) → HTTP/süre: $(cat "${TMPDIR:-/tmp}/chaos-c-me1.txt") sn (200 beklenir: istek sağ çıktı)"
say "API /health tekrar 200: $(wait_health 60) ms sonra"
sleep 2

title "Aşama 2 — uzun donma (30 sn)"
docker pause "$DB" >/dev/null
T0=$(now_ms)
say "donukken GET /me (en fazla 45 sn bekleniyor, zaman aşımı 25 sn) …"
R=$(ME)
say "donukken GET /me → HTTP/süre: $R sn (503 ve ≈25 sn beklenir: askıda KALMADI)"
LEFT=$(( 30000 - ( $(now_ms) - T0 ) ))
[ "$LEFT" -gt 0 ] && sleep $(( LEFT / 1000 + 1 ))
docker unpause "$DB" >/dev/null
say "API /health tekrar 200: $(wait_health 60) ms sonra"
sleep 1
stop_probe

title "Sonuçlar"
summarize_probe "$LOG"
AFTER=$(backend_started_at)
if [ "$BEFORE" = "$AFTER" ]; then say "backend süreci YENİDEN BAŞLAMADI: PASS"; else say "backend yeniden başlamış: FAIL"; fi
say "toparlanma sonrası GET /me → HTTP $(ME | cut -d' ' -f1) (200 beklenir)"

final_checks
