-- +goose Up
-- How drive time to an in-person meeting is worked out: 'fixed' uses fixed_drive_minutes,
-- 'google' asks the Routes API (fixed_drive_minutes is then the fallback when it cannot).
ALTER TABLE event_types ADD COLUMN travel_mode TEXT NOT NULL DEFAULT 'fixed' CHECK (travel_mode IN ('fixed', 'google'));
-- Daily count of Routes API lookups, so a runaway page cannot run up a Google bill.
CREATE TABLE bookmeet_route_usage (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  day TEXT NOT NULL,
  count INTEGER NOT NULL
);
-- +goose Down
DROP TABLE bookmeet_route_usage;
ALTER TABLE event_types DROP COLUMN travel_mode;
