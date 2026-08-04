-- Initial schema: port of the Prisma/PostgreSQL schema from cadence-cards-svelte.
-- SERIAL -> INTEGER PRIMARY KEY AUTOINCREMENT, TEXT[] -> JSON array TEXT,
-- enums -> TEXT CHECK, TIMESTAMP -> RFC3339 UTC TEXT.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT,
    email         TEXT UNIQUE,
    password_hash TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE topics (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    -- Claude prompt configuration (see internal/claude/prompts.go)
    topic_description TEXT,
    expertise         TEXT,
    focus             TEXT,
    context_type      TEXT,
    example           TEXT,
    question          TEXT,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (name, user_id)
);
CREATE INDEX idx_topics_user_id ON topics(user_id);

CREATE TABLE decks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    topic_id         INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    field1_label     TEXT,
    field2_label     TEXT,
    is_bidirectional INTEGER NOT NULL DEFAULT 0 CHECK (is_bidirectional IN (0,1)),
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (name, topic_id)
);
CREATE INDEX idx_decks_topic_id ON decks(topic_id);

CREATE TABLE cards (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    deck_id    INTEGER NOT NULL REFERENCES decks(id) ON DELETE CASCADE,
    front      TEXT NOT NULL,
    back       TEXT NOT NULL,
    note       TEXT,
    priority   TEXT NOT NULL CHECK (priority IN ('A','B','C')),
    tags       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
    version    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_cards_deck_id ON cards(deck_id);

-- One schedule per study direction of a card (is_reversed 0 = front->back).
CREATE TABLE schedules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    card_id     INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    is_reversed INTEGER NOT NULL DEFAULT 0 CHECK (is_reversed IN (0,1)),
    easiness    REAL    NOT NULL DEFAULT 2.5,
    interval    INTEGER NOT NULL DEFAULT 1,
    rep_count   INTEGER NOT NULL DEFAULT 0,
    grade       TEXT CHECK (grade IS NULL OR grade IN
                  ('INCORRECT','CORRECT_WITH_HESITATION','CORRECT_PERFECT_RECALL')),
    last_seen   TEXT,
    version     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (card_id, is_reversed)
);
CREATE INDEX idx_schedules_card_id ON schedules(card_id);

-- Server-side login sessions; the cookie holds the raw token, the DB only its hash.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);
