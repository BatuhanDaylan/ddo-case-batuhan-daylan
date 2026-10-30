-- +goose Up
-- Denklik: "course_id yerine substitute_course_id sayılır". Öğrenci substitute dersini geçtiyse
-- course_id gereksinimi (ön koşul, müfredat, mezuniyet) sağlanmış kabul edilir. Tek seviyelidir
-- (A≡B ve B≡C, A≡C anlamına gelmez); zincirleme istenirse açıkça kayıt girilir.
CREATE TABLE course_equivalences (
    id                   BIGSERIAL PRIMARY KEY,
    course_id            BIGINT NOT NULL REFERENCES courses(id),
    substitute_course_id BIGINT NOT NULL REFERENCES courses(id),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (course_id, substitute_course_id),
    CHECK (course_id <> substitute_course_id)
);
CREATE INDEX course_equivalences_substitute_idx ON course_equivalences (substitute_course_id);

-- Muafiyet: ders başarılmış sayılır ama notu yoktur; GANO hesabına girmez.
ALTER TABLE course_results ADD COLUMN exempt BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE course_results DROP COLUMN exempt;
DROP TABLE course_equivalences;
