-- +goose Up
-- The guest's side of a personal link: the conversation, kept so closing the page and coming
-- back continues it, and two facts about where the guest is in it.
--
-- details_done marks that the email and phone step is finished (the phone is optional, so
-- "has a phone number" cannot stand in for it). guest_timezone is the zone the guest's
-- browser reported, used to show times and to tell the owner what time it is for the guest.
ALTER TABLE personal_links ADD COLUMN details_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE personal_links ADD COLUMN guest_timezone TEXT NOT NULL DEFAULT '';

-- kind says how to draw the entry: plain text, or a card (the open times, the confirm step,
-- the booked meeting) whose contents are JSON in `content`.
CREATE TABLE personal_link_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id    TEXT NOT NULL REFERENCES personal_links(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('guest','assistant')),
    kind       TEXT NOT NULL DEFAULT 'text',
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_personal_link_messages_link ON personal_link_messages(link_id, id);

-- +goose Down
DROP TABLE personal_link_messages;
ALTER TABLE personal_links DROP COLUMN guest_timezone;
ALTER TABLE personal_links DROP COLUMN details_done;
