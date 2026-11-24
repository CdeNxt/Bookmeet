-- +goose Up
-- A decision waiting on the owner's tap, sent to them by email because they have no card and
-- no text channel to be asked on: a time that does not book as things stand, a link with such
-- times, or times a guest suggested. The link in the email carries a random token; only its
-- hash is kept. The token alone approves nothing: the owner must also be signed in. payload
-- is what exactly would be done. outcome is empty until it is answered, once.
CREATE TABLE owner_email_approvals (
    id         TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('direct','link','suggestion')),
    payload    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    outcome    TEXT NOT NULL DEFAULT '',
    decided_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_owner_email_approvals_user ON owner_email_approvals(user_id, created_at);

-- +goose Down
DROP TABLE owner_email_approvals;
