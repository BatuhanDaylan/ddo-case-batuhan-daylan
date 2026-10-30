#!/bin/sh
# Kaos betikleri için ortak yardımcılar (POSIX sh; Linux, macOS ve Windows Git Bash'te çalışır).
# Gereksinim: ayakta bir `docker compose up -d` ortamı ve curl.
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
API="${API:-http://localhost:8080}"
PROBE_PID=""

now_ms() { date +%s%3N; }
title() { printf '\n=== %s ===\n' "$1"; }
say() { printf '  %s\n' "$*"; }

need_stack() {
  if ! curl -sf --max-time 3 "$API/health" >/dev/null; then
    echo "Sistem ayakta değil ($API/health). Önce: docker compose up -d --build" >&2
    exit 2
  fi
}

# Simülasyon verisi (12.000 öğrenci) yoksa yükler; varsa dokunmaz (idempotent).
seed_data() {
  docker compose --profile sim run --rm -T simulator seed 2>&1 | tail -1 | sed 's/^/  /'
}

wait_health() { # $1 = en fazla bekleme (sn). Başarılıysa geçen ms'yi basar.
  t0=$(now_ms); limit=$(( $1 * 1000 ))
  while :; do
    if curl -sf --max-time 2 "$API/health" >/dev/null 2>&1; then echo $(( $(now_ms) - t0 )); return 0; fi
    [ $(( $(now_ms) - t0 )) -gt "$limit" ] && { echo -1; return 1; }
    sleep 0.2
  done
}

# /health'i 200 ms aralıkla yoklar; her satır: "<epoch ms> <http kodu> <süre ms>" (kod 000 = bağlantı yok/zaman aşımı).
start_probe() {
  : > "$1"
  (
    while :; do
      s=$(now_ms)
      code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$API/health" 2>/dev/null || true)
      [ -z "$code" ] && code=000
      echo "$s $code $(( $(now_ms) - s ))" >> "$1"
      sleep 0.2
    done
  ) &
  PROBE_PID=$!
}
stop_probe() { [ -n "$PROBE_PID" ] && kill "$PROBE_PID" 2>/dev/null || true; wait "$PROBE_PID" 2>/dev/null || true; PROBE_PID=""; }

# Yoklama günlüğünden kesinti penceresini ve istatistikleri özetler.
summarize_probe() {
  awk '
    { n++; if ($2 == 200) ok++; else { bad++; if (!first) first = $1; last = $1 }
      if ($3 > maxd) maxd = $3 }
    END {
      printf "  yoklama: %d istek, %d başarılı, %d başarısız\n", n, ok, bad
      if (bad > 0) printf "  kesinti penceresi: ilk hata → son hata = %d ms; en uzun tek yoklama %d ms\n", last - first, maxd
      else printf "  hiç kesinti görülmedi; en uzun tek yoklama %d ms\n", maxd
    }' "$1"
}

backend_started_at() { docker inspect -f '{{.State.StartedAt}}' "$(docker compose ps -q backend)"; }

final_checks() {
  title "Toparlanma doğrulaması"
  wait_health 60 >/dev/null || say "UYARI: API 60 sn içinde sağlıklı olmadı"
  sleep 2 # veritabanının bağlantı kabul etmesi için kısa pay

  say "kota ve tutarlılık denetimi (veritabanı):"
  out=$(docker compose --profile sim run --rm -T simulator check 2>&1 || true)
  if echo "$out" | grep -qE 'PASS|FAIL'; then
    echo "$out" | grep -E 'PASS|FAIL' | sed 's/^/    /'
  else
    # Çıktıda PASS/FAIL yoksa denetim ÇALIŞMAMIŞTIR: bunu sessizce yutmak yanlış güvence verirdi.
    say "FAIL: denetim sonuç üretmedi (çalışmamış olabilir). Ham çıktı:"
    echo "$out" | tail -5 | sed 's/^/    /'
  fi

  say "tam doğrulama (make verify):"
  out=$(docker compose --profile verify run --rm -T verify 2>&1 || true)
  echo "$out" | grep -E 'Toplam|FAIL' | sed 's/^/    /'
  echo "$out" | grep -q 'Toplam' || { say "FAIL: doğrulama sonuç üretmedi. Ham çıktı:"; echo "$out" | tail -5 | sed 's/^/    /'; }
}
