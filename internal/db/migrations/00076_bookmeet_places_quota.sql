-- +goose Up
CREATE TABLE bookmeet_place_search_usage (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  day TEXT NOT NULL,
  count INTEGER NOT NULL
);
-- +goose Down
DROP TABLE bookmeet_place_search_usage;
