-- Setup wizard (docs/setup-wizard-design.md section 6). The account table is rebuilt because the
-- cross-column invariant (open mode never has a web password) needs a table-level CHECK, which
-- ALTER TABLE ADD COLUMN cannot add. It holds at most one row and no foreign key points at it.
--
-- auth_mode: 'standard' is a web password, or none with Cloudflare Access (design.md 7.0);
-- 'open' is no password at all, fenced by the open gate. created_via: 'env' (KIPPLE_USERNAME /
-- KIPPLE_PASSWORD on first start) or 'wizard' (the browser setup).
CREATE TABLE account_new (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  username          TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64
                                         AND username NOT GLOB '*[^A-Za-z0-9._-]*'),
  password_hash     TEXT NOT NULL,
  api_password_hash TEXT,
  secret            TEXT NOT NULL CHECK (length(secret) = 64),
  auth_mode         TEXT NOT NULL DEFAULT 'standard' CHECK (auth_mode IN ('standard', 'open')),
  created_via       TEXT NOT NULL DEFAULT 'env' CHECK (created_via IN ('env', 'wizard')),
  created_at        INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at        INTEGER NOT NULL DEFAULT (unixepoch()),
  CHECK (auth_mode = 'standard' OR password_hash = '')
) STRICT;
INSERT INTO account_new (id, username, password_hash, api_password_hash, secret, created_at, updated_at)
  SELECT id, username, password_hash, api_password_hash, secret, created_at, updated_at FROM account;
DROP TABLE account;
ALTER TABLE account_new RENAME TO account;

-- An existing account has been "set up" already: it must never see onboarding.
INSERT INTO settings (key, value)
  SELECT 'sys.setup_completed_at', CAST(unixepoch() AS TEXT) FROM account WHERE id = 1
  ON CONFLICT (key) DO NOTHING;
-- Existing installs keep their old defaults (owner decisions 1 and 5): the time zone stays
-- America/New_York unless one is already set, and an unset KIPPLE_ADDR keeps the pre-0.5 port
-- (the fallback was removed in 0.6.0). A database without an account was never usable and is treated as fresh (UTC, 1919).
INSERT INTO settings (key, value)
  SELECT 'tz', '"America/New_York"' FROM account WHERE id = 1
  ON CONFLICT (key) DO NOTHING;
INSERT INTO settings (key, value)
  SELECT 'sys.legacy_port', 'true' FROM account WHERE id = 1
  ON CONFLICT (key) DO NOTHING;
