-- +goose Up
CREATE TABLE terms (
    id                BIGSERIAL PRIMARY KEY,
    code              TEXT    NOT NULL UNIQUE,
    name              TEXT    NOT NULL,
    registration_open BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE sections (
    id             BIGSERIAL PRIMARY KEY,
    term_id        BIGINT  NOT NULL REFERENCES terms(id),
    course_id      BIGINT  NOT NULL REFERENCES courses(id),
    code           TEXT    NOT NULL,
    instructor_id  BIGINT  REFERENCES users(id),
    capacity       INTEGER NOT NULL CHECK (capacity > 0),
    -- enrolled_count son savunma hattıdır: uygulama hatası olsa bile kontenjan
    -- veritabanı düzeyinde aşılamaz (CHECK ihlali transaction'ı geri alır).
    enrolled_count INTEGER NOT NULL DEFAULT 0,
    UNIQUE (term_id, course_id, code),
    CONSTRAINT sections_quota_ok CHECK (enrolled_count >= 0 AND enrolled_count <= capacity)
);
CREATE INDEX sections_term_idx ON sections (term_id, id);

-- Bir kayıt satırı hem koltuğu (enrolled) hem bekleme sırasını (waitlisted) temsil eder.
-- Bekleme sırası id sırasıdır: id'ler şube satır kilidi altında verildiği için
-- "talebin kilidi alma sırası" = "id sırası"dır.
CREATE TABLE enrollments (
    id          BIGSERIAL PRIMARY KEY,
    section_id  BIGINT      NOT NULL REFERENCES sections(id),
    student_id  BIGINT      NOT NULL REFERENCES students(id),
    status      TEXT        NOT NULL CHECK (status IN ('enrolled', 'waitlisted', 'dropped')),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    promoted_at TIMESTAMPTZ,
    dropped_at  TIMESTAMPTZ
);
-- Bir öğrenci aynı şubede aynı anda yalnızca bir aktif kayda sahip olabilir.
CREATE UNIQUE INDEX enrollments_active_uniq ON enrollments (section_id, student_id)
    WHERE status IN ('enrolled', 'waitlisted');
-- Bekleme listesinin ilk sırasını bulmak için.
CREATE INDEX enrollments_waitlist_idx ON enrollments (section_id, id) WHERE status = 'waitlisted';
CREATE INDEX enrollments_student_idx ON enrollments (student_id, id);

-- İdempotans: aynı (kullanıcı, anahtar) ile gelen tekrar istek aynı yanıtı alır.
CREATE TABLE idempotency_keys (
    user_id       BIGINT      NOT NULL REFERENCES users(id),
    key           TEXT        NOT NULL,
    fingerprint   TEXT        NOT NULL,
    status_code   INTEGER,
    response_body BYTEA,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE enrollments;
DROP TABLE sections;
DROP TABLE terms;
