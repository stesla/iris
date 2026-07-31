-- +goose Up
CREATE TABLE upstreams (
       name     TEXT PRIMARY KEY,
       address  TEXT,
       bcrypt   TEXT,
       script   TEXT
);

-- +goose Down
DROP TABLE upstreams;
