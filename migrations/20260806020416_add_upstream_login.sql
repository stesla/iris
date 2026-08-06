-- +goose Up
CREATE TABLE new_upstreams (
       name     TEXT PRIMARY KEY,
       address  TEXT NOT NULL,
       login    TEXT NOT NULL,
       bcrypt   TEXT NOT NULL,
       script   TEXT

);

INSERT INTO new_upstreams (name, address, login, bcrypt) SELECT
       name,
       address,
       REPLACE(REPLACE(script,"connect ",""), " %PASSWORD%", ""),
       bcrypt
FROM upstreams;

DROP TABLE upstreams;

ALTER TABLE new_upstreams RENAME TO upstreams;

-- +goose Down
UPDATE upstreams SET script=CONCAT("connect ", login, " %PASSWORD%");
ALTER TABLE upstreams DROP COLUMN login;
