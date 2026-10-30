.PHONY: up down test lint reset-demo load verify

COMPOSE ?= docker compose
GO_IMAGE ?= golang:1.26-alpine
NET ?= $(notdir $(CURDIR))_default
TESTDB ?= postgres://uninet:change-me-locally@db:5432/uninet?sslmode=disable

up:
	cp -n .env.example .env || true
	$(COMPOSE) up --build -d

down:
	$(COMPOSE) down

# Go yerelde kurulu olmak zorunda değil: testler konteynerde, çalışan veritabanına karşı koşar.
test:
	$(COMPOSE) up -d db
	docker run --rm --network $(NET) -e TEST_DATABASE_URL="$(TESTDB)" \
		-v "$(CURDIR)/backend:/src" -w /src $(GO_IMAGE) go test -count=1 -cover ./...

lint:
	docker run --rm -v "$(CURDIR)/backend:/src" -w /src $(GO_IMAGE) \
		sh -c 'test -z "$$(gofmt -l .)" && go vet ./...'

# Verileri ilk seed haline döndürür (volume silinir, migrasyon + seed yeniden çalışır).
reset-demo:
	$(COMPOSE) down -v
	$(COMPOSE) up --build -d

# Uzun yük testi (k6); make verify içinde değildir.
load:
	$(COMPOSE) --profile load run --rm k6

# Tüm özellikleri API üzerinden uçtan uca dener ve PASS/FAIL tablosu basar.
verify:
	$(COMPOSE) --profile verify run --rm verify
