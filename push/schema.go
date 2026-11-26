package push

// SchemaSQL is the DDL for the tables this package reads and writes.
//
// Execute it in your own migration tool, in your own migration history, with
// your own rollback semantics. The package ships no migrations of its own and
// never runs this itself: imposing a migration tool is the surest way to be
// refused by a team that did not choose it.
//
// A schema change arrives as a NEW migration in your repository. Never edit an
// applied one — the checksum will not match and the generated rollback no
// longer describes what is there.
//
// No foreign keys to your own tables. The account reference is opaque here, and
// a notification row outliving the account it mentions is a smaller problem
// than a migration that cannot run because of an ordering you did not choose.
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS push_tokens (
    token       TEXT PRIMARY KEY,
    account_ref TEXT        NOT NULL,
    device_key  BYTEA,
    platform    TEXT        NOT NULL,
    language    TEXT        NOT NULL DEFAULT 'en',
    status      TEXT        NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Every send begins by asking which handsets an account has.
CREATE INDEX IF NOT EXISTS push_tokens_account_idx
    ON push_tokens (account_ref);

-- Hosts that address one handset of an account look it up this way. Partial,
-- because hosts without that concept leave device_key null and should not pay
-- for an index they never read.
CREATE INDEX IF NOT EXISTS push_tokens_account_device_idx
    ON push_tokens (account_ref, device_key)
    WHERE device_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS push_preferences (
    account_ref TEXT        NOT NULL,
    category    TEXT        NOT NULL,
    enabled     BOOLEAN     NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_ref, category)
);

CREATE TABLE IF NOT EXISTS push_copy (
    kind       TEXT        NOT NULL,
    language   TEXT        NOT NULL,
    title      TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, language)
);

-- Wording can be changed without a deploy, so every change says who and when.
CREATE TABLE IF NOT EXISTS push_copy_audit (
    id         BIGSERIAL PRIMARY KEY,
    kind       TEXT        NOT NULL,
    language   TEXT        NOT NULL,
    title      TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    actor      TEXT        NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS push_copy_audit_kind_idx
    ON push_copy_audit (kind, changed_at DESC);

-- Only for a host using the reference poller. A host with a real broker puts
-- its own Deferred implementation in front of that broker and never creates
-- this table.
--
-- claimed_at is a lease, not a flag: a poller that dies between claiming a job
-- and finishing it would otherwise strand that job forever, and the symptom is
-- one notification that never arrives with nothing logged anywhere.
CREATE TABLE IF NOT EXISTS push_jobs (
    id          TEXT PRIMARY KEY,
    kind        TEXT        NOT NULL,
    account_ref TEXT        NOT NULL,
    device_key  BYTEA,
    fields      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    first_seen  TIMESTAMPTZ NOT NULL,
    attempts    INTEGER     NOT NULL DEFAULT 0,
    run_at      TIMESTAMPTZ NOT NULL,
    claimed_at  TIMESTAMPTZ
);

-- Every poll asks for due, unclaimed work in run_at order.
CREATE INDEX IF NOT EXISTS push_jobs_due_idx
    ON push_jobs (run_at, claimed_at);
`
