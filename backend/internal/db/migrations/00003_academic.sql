-- +goose Up
CREATE TABLE faculties (
    id   BIGSERIAL PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL
);

CREATE TABLE departments (
    id         BIGSERIAL PRIMARY KEY,
    faculty_id BIGINT NOT NULL REFERENCES faculties(id),
    code       TEXT   NOT NULL UNIQUE,
    name       TEXT   NOT NULL
);
CREATE INDEX departments_faculty_idx ON departments (faculty_id);

CREATE TABLE programs (
    id                  BIGSERIAL PRIMARY KEY,
    department_id       BIGINT  NOT NULL REFERENCES departments(id),
    code                TEXT    NOT NULL UNIQUE,
    name                TEXT    NOT NULL,
    total_ects_required INTEGER NOT NULL CHECK (total_ects_required > 0)
);
CREATE INDEX programs_department_idx ON programs (department_id);

CREATE TABLE courses (
    id            BIGSERIAL PRIMARY KEY,
    code          TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    ects          INTEGER NOT NULL CHECK (ects > 0 AND ects <= 60),
    department_id BIGINT REFERENCES departments(id)
);

-- Müfredat sürümü: bir program için belirli bir yıldan itibaren geçerli ders listesi.
-- Eski sürümler asla değiştirilmez/silinmez; yeni müfredat yeni bir satırdır.
CREATE TABLE curriculum_versions (
    id             BIGSERIAL PRIMARY KEY,
    program_id     BIGINT  NOT NULL REFERENCES programs(id),
    effective_year INTEGER NOT NULL CHECK (effective_year BETWEEN 1990 AND 2100),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (program_id, effective_year)
);

CREATE TABLE curriculum_courses (
    curriculum_version_id BIGINT  NOT NULL REFERENCES curriculum_versions(id) ON DELETE CASCADE,
    course_id             BIGINT  NOT NULL REFERENCES courses(id),
    semester              INTEGER NOT NULL CHECK (semester BETWEEN 1 AND 12),
    kind                  TEXT    NOT NULL CHECK (kind IN ('required', 'elective')),
    PRIMARY KEY (curriculum_version_id, course_id)
);
CREATE INDEX curriculum_courses_course_idx ON curriculum_courses (course_id);

-- Öğrenci hangi yıl girdiyse o yılın müfredatına tabidir (entry_year).
CREATE TABLE students (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT  NOT NULL UNIQUE REFERENCES users(id),
    student_no TEXT    NOT NULL UNIQUE,
    program_id BIGINT  NOT NULL REFERENCES programs(id),
    entry_year INTEGER NOT NULL CHECK (entry_year BETWEEN 1990 AND 2100),
    advisor_id BIGINT REFERENCES users(id)
);
CREATE INDEX students_program_idx ON students (program_id, entry_year);
CREATE INDEX students_advisor_idx ON students (advisor_id);

-- +goose Down
DROP TABLE students;
DROP TABLE curriculum_courses;
DROP TABLE curriculum_versions;
DROP TABLE courses;
DROP TABLE programs;
DROP TABLE departments;
DROP TABLE faculties;
