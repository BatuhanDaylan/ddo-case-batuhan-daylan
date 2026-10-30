#!/bin/sh
# Kaos senaryosu B: API ile veritabanı arasına AĞ GECİKMESİ (Toxiproxy ile).
#
# Hipotez: API ↔ PostgreSQL arasına 150 ms (±50 ms) gecikme girince
#   (1) doğruluk KORUNUR: kontenjan aşımı yok, sayaçlar/sıra tutarlı, 5xx yok,
#   (2) gecikme ve verim BEKLENEN biçimde kötüleşir (işlem başına birkaç gidiş-dönüş × gecikme).
#
# Yöntem: Toxiproxy'yi veritabanının önüne koyup ikinci bir API örneği (chaos-api, 8081) onun üzerinden bağlanır;
# normal API'ye dokunulmaz. Aynı yük (aynı öğrenci sayısı) önce gecikmesiz, sonra gecikmeli koşulur.
. "$(dirname "$0")/lib.sh"

title "B) API ↔ veritabanı arasında ağ gecikmesi (Toxiproxy)"
need_stack
seed_data

NET=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(docker compose ps -q db)")
set -a; . ./.env; set +a
IMG=$(docker inspect -f '{{.Config.Image}}' "$(docker compose ps -q backend)")

cleanup() { docker rm -f chaos-toxiproxy chaos-api >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
cleanup

docker run -d --name chaos-toxiproxy --network "$NET" -p 8474:8474 ghcr.io/shopify/toxiproxy:2.9.0 >/dev/null
for i in 1 2 3 4 5 6 7 8 9 10; do curl -sf http://localhost:8474/version >/dev/null 2>&1 && break; sleep 1; done
curl -sf -X POST http://localhost:8474/proxies -d '{"name":"db","listen":"0.0.0.0:5433","upstream":"db:5432"}' >/dev/null
say "Toxiproxy hazır: chaos-toxiproxy:5433 → db:5432"

docker run -d --name chaos-api --network "$NET" -p 8081:8080 \
  -e "DATABASE_URL=postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@chaos-toxiproxy:5433/${POSTGRES_DB}?sslmode=disable" \
  -e "JWT_SECRET=${JWT_SECRET}" -e "DOC_SIGNING_KEY=${DOC_SIGNING_KEY}" -e SEED_DEMO=false "$IMG" >/dev/null
API=http://localhost:8081 wait_health 60 >/dev/null
say "chaos-api (8081) Toxiproxy üzerinden veritabanına bağlı ve sağlıklı"

run_storm() { # $1 = offset
  docker compose --profile sim run --rm -T simulator storm -api http://chaos-api:8080 -students 300 -per-student 3 -concurrency 40 -offset "$1" 2>&1
}

title "Taban çizgisi (gecikme YOK)"
BASE=$(run_storm 6000); echo "$BASE" | sed 's/^/  /'

curl -sf -X POST http://localhost:8474/proxies/db/toxics \
  -d '{"name":"gecikme","type":"latency","attributes":{"latency":150,"jitter":50}}' >/dev/null
title "Gecikmeli (150 ms ± 50 ms, her yönde)"
SLOW=$(run_storm 6300); echo "$SLOW" | sed 's/^/  /'

title "Karşılaştırma"
P95_BASE=$(echo "$BASE" | sed -n 's/.*p95=\([^ ]*\).*/\1/p')
P95_SLOW=$(echo "$SLOW" | sed -n 's/.*p95=\([^ ]*\).*/\1/p')
TP_BASE=$(echo "$BASE" | sed -n 's/.*verim: \([0-9]*\).*/\1/p')
TP_SLOW=$(echo "$SLOW" | sed -n 's/.*verim: \([0-9]*\).*/\1/p')
say "gecikme p95:   $P95_BASE  →  $P95_SLOW"
say "verim (istek/sn): $TP_BASE  →  $TP_SLOW"
E5=$(echo "$SLOW" | grep -E 'HTTP 5[0-9][0-9]' || true)
if [ -z "$E5" ]; then say "gecikmeli koşuda 5xx yok: PASS"; else say "gecikmeli koşuda 5xx var: FAIL"; echo "$E5"; fi

cleanup; trap - EXIT INT TERM
final_checks
