-- +goose Up
-- What the meeting is about, in the owner's words. Shown to the guest when the link opens.
ALTER TABLE personal_links ADD COLUMN topic TEXT NOT NULL DEFAULT '';

-- The details panel the owner is looking at in Telegram: what the assistant understood,
-- waiting for the owner's tap. One open draft per owner; `data` is the panel's contents.
CREATE TABLE assistant_drafts (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL DEFAULT 0,
    data       TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','discarded')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_assistant_drafts_user ON assistant_drafts(user_id, status);

-- The owner's conversation with the assistant, so each message is read in context. Trimmed
-- as it grows; this is working memory, not a record.
CREATE TABLE assistant_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('user','assistant','event')),
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_assistant_messages_user ON assistant_messages(user_id, id);

-- +goose Down
DROP TABLE assistant_messages;
DROP TABLE assistant_drafts;
ALTER TABLE personal_links DROP COLUMN topic;
