-- +goose Up
-- Which connected account wrote a booking's calendar event. Reading the event back (to follow
-- a move or deletion made in the host's calendar) trusts "no such event" only from that account.
ALTER TABLE booking_hosts ADD COLUMN external_account TEXT;

-- Events written before this was recorded: assume the host's current destination account wrote
-- them, but only where that is unambiguous (the host has exactly one connection for the
-- event's provider, or exactly one connection at all when the provider was never stamped).
UPDATE booking_hosts
SET external_account = (
    SELECT cc.account_email FROM calendar_connections cc
    WHERE cc.user_id = booking_hosts.user_id AND cc.is_destination = 1
      AND cc.provider = COALESCE(NULLIF(booking_hosts.external_provider, ''), cc.provider)
      AND (SELECT COUNT(*) FROM calendar_connections c2
           WHERE c2.user_id = booking_hosts.user_id
             AND c2.provider = COALESCE(NULLIF(booking_hosts.external_provider, ''), c2.provider)) = 1
      AND (COALESCE(booking_hosts.external_provider, '') != ''
           OR (SELECT COUNT(*) FROM calendar_connections c3 WHERE c3.user_id = booking_hosts.user_id) = 1)
    LIMIT 1)
WHERE COALESCE(external_event_id, '') != '';

-- +goose Down
ALTER TABLE booking_hosts DROP COLUMN external_account;
