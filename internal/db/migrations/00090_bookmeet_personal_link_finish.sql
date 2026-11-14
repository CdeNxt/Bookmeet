-- +goose Up
-- The language the guest's page was shown in, so a card written for them later (the owner
-- approving a time they suggested) and their booking's reminders match what they read.
ALTER TABLE personal_links ADD COLUMN guest_locale TEXT NOT NULL DEFAULT '';

-- When the owner was told a link ran out unused. Empty until then, so they are told once.
ALTER TABLE personal_links ADD COLUMN expiry_told_at TEXT NOT NULL DEFAULT '';

-- Links that had already run out before this existed are old news: do not announce them.
UPDATE personal_links
SET expiry_told_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE expires_at <= strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- +goose Down
ALTER TABLE personal_links DROP COLUMN expiry_told_at;
ALTER TABLE personal_links DROP COLUMN guest_locale;
