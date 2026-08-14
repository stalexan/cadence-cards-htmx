-- Pre-generated study questions (nightly Message Batches job). One unused
-- question per schedule (the PK is the uniqueness constraint); consumed on
-- serve, invalidated on card edit and Reset Progress, cascade-deleted with
-- the schedule.

CREATE TABLE generated_questions (
    schedule_id  INTEGER PRIMARY KEY REFERENCES schedules(id) ON DELETE CASCADE,
    question     TEXT NOT NULL,
    model        TEXT NOT NULL,
    generated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
