-- +goose Up
-- Roller sabit bir küme; CHECK ile veritabanı düzeyinde de korunur.
CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    full_name     TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN (
        'student', 'instructor', 'advisor', 'dept_secretary', 'registrar', 'reporting'
    )),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE users;
