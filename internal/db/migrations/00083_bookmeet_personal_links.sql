-- +goose Up
-- A personal booking link: made by an owner for one named guest, limited to the windows the
-- owner chose (each day can have its own), usable once. The booking assistant creates these
-- and the guest's chat page opens from them.
--
-- token_hash finds the link from the URL; token_enc keeps the same token recoverable so the
-- owner can be shown the link again when a guest loses it. Timestamps are RFC 3339 in UTC,
-- which is what makes the string comparisons on expires_at and updated_at valid.
CREATE TABLE personal_links (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type_id  TEXT NOT NULL REFERENCES event_types(id) ON DELETE CASCADE,
    token_hash     TEXT NOT NULL UNIQUE,
    token_enc      TEXT NOT NULL,
    guest_name     TEXT NOT NULL,
    guest_email    TEXT NOT NULL DEFAULT '',
    guest_phone    TEXT NOT NULL DEFAULT '',
    -- In-person meetings: the place the owner confirmed. Empty for everything else.
    place_name     TEXT NOT NULL DEFAULT '',
    place_address  TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'open'
                   CHECK (status IN ('open','proposed','followup','claiming','booked','expired','cancelled','replaced')),
    -- The status a link held before a booking attempt claimed it, so a failed attempt can
    -- put it back.
    claimed_from   TEXT NOT NULL DEFAULT '',
    booking_id     TEXT REFERENCES bookings(id) ON DELETE SET NULL,
    replaces_id    TEXT REFERENCES personal_links(id) ON DELETE SET NULL,
    expires_at     TEXT NOT NULL,
    last_opened_at TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE INDEX idx_personal_links_user ON personal_links(user_id, created_at);

CREATE TABLE personal_link_windows (
    id       TEXT PRIMARY KEY,
    link_id  TEXT NOT NULL REFERENCES personal_links(id) ON DELETE CASCADE,
    start_at TEXT NOT NULL,
    end_at   TEXT NOT NULL
);
CREATE INDEX idx_personal_link_windows_link ON personal_link_windows(link_id, start_at);

-- +goose Down
DROP TABLE personal_link_windows;
DROP TABLE personal_links;
