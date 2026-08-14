-- Server-side chat transcripts. The browser carries only a conversation ID;
-- the transcript itself lives here, so history can no longer be forged
-- client-side and request bodies stop growing with turn count.

CREATE TABLE conversations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    topic_id    INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    -- NULL = free-form topic chat; set = study chat about that schedule.
    schedule_id INTEGER REFERENCES schedules(id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_conversations_topic_id ON conversations(topic_id);
CREATE INDEX idx_conversations_schedule_id ON conversations(schedule_id);
CREATE INDEX idx_conversations_updated_at ON conversations(updated_at);

CREATE TABLE chat_messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL CHECK (role IN ('user','assistant')),
    content         TEXT NOT NULL,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_chat_messages_conversation_id ON chat_messages(conversation_id);
