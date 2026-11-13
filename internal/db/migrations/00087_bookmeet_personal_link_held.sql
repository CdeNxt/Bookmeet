-- +goose Up
-- What a guest wanted passed on to the owner, and where the guest's chat stands.
--
-- Only a booking reaches the owner the moment it happens. Everything else - times the guest
-- suggested, a change they asked for, a note, "I'll follow up", "I can't make it" - is held
-- here and sent as one Telegram message when the chat is over.
--
-- kind 'suggestions' holds a JSON array of RFC 3339 start times; every other kind holds the
-- guest's own words. reported_at and message_id say when it went to the owner and which
-- Telegram message carries its buttons. outcome is what the owner did with it.
CREATE TABLE personal_link_held (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id     TEXT NOT NULL REFERENCES personal_links(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('suggestions','change','note','followup','cannot')),
    content     TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    reported_at TEXT NOT NULL DEFAULT '',
    message_id  INTEGER NOT NULL DEFAULT 0,
    outcome     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_personal_link_held_link ON personal_link_held(link_id, id);

-- chat_mode: '' in the ordinary flow, 'suggest' while the guest is naming other times,
-- 'closing' once they have been asked if there is anything else, 'ended' when they said no.
-- chat_active_at is the guest's last action, chat_left_at when their page closed, and
-- chat_reported_at when the owner was last told how the chat ended. Together they decide
-- when a chat is over. Timestamps are RFC 3339 in UTC, so they compare as strings.
ALTER TABLE personal_links ADD COLUMN chat_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE personal_links ADD COLUMN chat_active_at TEXT NOT NULL DEFAULT '';
ALTER TABLE personal_links ADD COLUMN chat_left_at TEXT NOT NULL DEFAULT '';
ALTER TABLE personal_links ADD COLUMN chat_reported_at TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE personal_links DROP COLUMN chat_reported_at;
ALTER TABLE personal_links DROP COLUMN chat_left_at;
ALTER TABLE personal_links DROP COLUMN chat_active_at;
ALTER TABLE personal_links DROP COLUMN chat_mode;
DROP TABLE personal_link_held;
