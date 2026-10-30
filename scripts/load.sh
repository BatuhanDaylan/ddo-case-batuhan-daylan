#!/bin/sh
# T-1 yük testi uçtan uca: veri → token → k6 → veritabanı doğrulaması.
set -e
COMPOSE="docker compose"
$COMPOSE up -d --build
$COMPOSE --profile sim run --rm simulator seed
$COMPOSE --profile sim run --rm simulator tokens -n "${LOAD_STUDENTS:-5000}" -out /data/tokens.json
$COMPOSE --profile load run --rm -T k6
$COMPOSE --profile sim run --rm simulator check
