-- +goose Up
-- When the owner was told a link has no open times left (the guest found none, or the sweep
-- noticed). Empty until then, so they are told once per link.
ALTER TABLE personal_links ADD COLUMN empty_told_at TEXT NOT NULL DEFAULT '';

-- A link's windows are now the hours for that meeting: links offer every time inside them
-- that the owner is not busy for, when the owner allows exceptions (the setting, on by
-- default). Links still open when this arrived get the same, so a link made under the old
-- rule stops hiding times its owner chose to offer.
UPDATE personal_links SET outside_availability = 1
WHERE status IN ('open', 'proposed', 'followup')
  AND (SELECT assistant_exceptions FROM server_settings WHERE id = 1) = 1;

-- +goose Down
ALTER TABLE personal_links DROP COLUMN empty_told_at;
