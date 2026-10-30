-- +goose Up
-- Ön koşul: ders başına bir ifade ağacı (JSON), örn. A ve (B veya C).
-- {"op":"and","args":[{"course_id":1},{"op":"or","args":[{"course_id":2},{"course_id":3}]}]}
CREATE TABLE course_prerequisites (
    course_id BIGINT PRIMARY KEY REFERENCES courses(id) ON DELETE CASCADE,
    expr      JSONB  NOT NULL
);

-- Şube ders saatleri. Gün 1=Pazartesi … 7=Pazar; saatler gece yarısından itibaren dakika.
CREATE TABLE section_meetings (
    id         BIGSERIAL PRIMARY KEY,
    section_id BIGINT  NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
    day        INTEGER NOT NULL CHECK (day BETWEEN 1 AND 7),
    start_min  INTEGER NOT NULL CHECK (start_min >= 0),
    end_min    INTEGER NOT NULL CHECK (end_min <= 1440),
    CHECK (start_min < end_min)
);
CREATE INDEX section_meetings_section_idx ON section_meetings (section_id);

-- Öğrencinin tamamlanmış ders sonuçları (transkript satırları). Not motoru (T-4) bunu genişletecek.
CREATE TABLE course_results (
    id          BIGSERIAL PRIMARY KEY,
    student_id  BIGINT  NOT NULL REFERENCES students(id),
    course_id   BIGINT  NOT NULL REFERENCES courses(id),
    term_id     BIGINT  NOT NULL REFERENCES terms(id),
    letter      TEXT    NOT NULL,
    grade_point NUMERIC(3, 2) NOT NULL CHECK (grade_point BETWEEN 0 AND 4),
    passed      BOOLEAN NOT NULL,
    UNIQUE (student_id, course_id, term_id)
);
CREATE INDEX course_results_student_idx ON course_results (student_id);

-- +goose Down
DROP TABLE course_results;
DROP TABLE section_meetings;
DROP TABLE course_prerequisites;
