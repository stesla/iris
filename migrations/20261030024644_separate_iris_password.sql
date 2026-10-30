-- +goose Up
-- Upstream passwords were bcrypt hashes that doubled as the Iris login. They
-- are now encrypted (not hashed) so Iris can send them to the game itself.
-- The old hashes can't be turned back into passwords, so every migrated
-- upstream starts with a NULL password that must be set again.
-- AUTOINCREMENT so a deleted upstream's id is never reused: live sessions
-- and encrypted passwords are tied to the id.
CREATE TABLE new_upstreams (
       id       INTEGER PRIMARY KEY AUTOINCREMENT,
       name     TEXT NOT NULL UNIQUE,
       address  TEXT NOT NULL,
       login    TEXT NOT NULL,
       password BLOB,
       script   TEXT
);

INSERT INTO new_upstreams (name, address, login, script) SELECT
       name,
       address,
       login,
       script
FROM upstreams;

DROP TABLE upstreams;

ALTER TABLE new_upstreams RENAME TO upstreams;

-- The single global Iris password. Moves into a users table once Iris
-- supports more than one user.
CREATE TABLE iris_auth (
       id            INTEGER PRIMARY KEY CHECK (id = 1),
       password_hash TEXT NOT NULL
);

-- +goose Down
-- Not truly reversible: the encrypted passwords can't be turned back into the
-- old bcrypt hashes, so every upstream comes back with an empty hash that no
-- password matches.
DROP TABLE iris_auth;

CREATE TABLE new_upstreams (
       name     TEXT PRIMARY KEY,
       address  TEXT NOT NULL,
       login    TEXT NOT NULL,
       bcrypt   TEXT NOT NULL,
       script   TEXT
);

INSERT INTO new_upstreams (name, address, login, bcrypt, script) SELECT
       name,
       address,
       login,
       '',
       script
FROM upstreams;

DROP TABLE upstreams;

ALTER TABLE new_upstreams RENAME TO upstreams;
