-- listmonk-plus migration 0001: event log and sequences schema.
-- See plus/docs/001-sequences-design.md (§5).
--
-- Status-like columns use TEXT + CHECK instead of Postgres enums so that new
-- values can be added later without enum migrations.

-- Append-only log of things that happened. Fed by triggers on upstream tables
-- (see ../triggers.sql) and by explicit hooks in Go. No foreign keys on purpose:
-- events must survive deletion of the subscriber/list they refer to.
CREATE TABLE IF NOT EXISTS plus_events (
    id              BIGSERIAL PRIMARY KEY,
    type            TEXT NOT NULL,
    subscriber_id   INTEGER NULL,
    list_id         INTEGER NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    processed_at    TIMESTAMP WITH TIME ZONE NULL
);
CREATE INDEX IF NOT EXISTS idx_plus_events_pending ON plus_events (id) WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_plus_events_sub ON plus_events (subscriber_id);

CREATE TABLE IF NOT EXISTS plus_sequences (
    id                    SERIAL PRIMARY KEY,
    uuid                  UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    name                  TEXT NOT NULL,
    status                TEXT NOT NULL DEFAULT 'draft'
                              CHECK (status IN ('draft', 'active', 'paused')),
    list_id               INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE ON UPDATE CASCADE,
    trigger_enabled       BOOLEAN NOT NULL DEFAULT true,
    reentry_after_exit    TEXT NOT NULL DEFAULT 'ignore'
                              CHECK (reentry_after_exit IN ('ignore', 'restart')),
    reentry_while_active  TEXT NOT NULL DEFAULT 'continue'
                              CHECK (reentry_while_active IN ('continue', 'restart')),
    from_email            TEXT NULL,
    created_at            TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
-- D5: at most one automated (trigger-enabled) sequence per list. Drafts don't count.
CREATE UNIQUE INDEX IF NOT EXISTS idx_plus_sequences_one_auto_per_list
    ON plus_sequences (list_id) WHERE trigger_enabled AND status IN ('active', 'paused');

CREATE TABLE IF NOT EXISTS plus_sequence_steps (
    id                SERIAL PRIMARY KEY,
    uuid              UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    sequence_id       INTEGER NOT NULL REFERENCES plus_sequences(id) ON DELETE CASCADE ON UPDATE CASCADE,
    position          INTEGER NOT NULL CHECK (position >= 1),
    -- Wait after the previous step was sent (step 1: after enrollment). 0 = immediately.
    delay             INTERVAL NOT NULL DEFAULT '0' CHECK (delay >= '0'),
    template_id       INTEGER NOT NULL REFERENCES templates(id) ON DELETE RESTRICT ON UPDATE CASCADE,
    subject           TEXT NULL,
    skip_landing_url  TEXT NULL,
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    -- Deferrable so that steps can be reordered in a single transaction.
    UNIQUE (sequence_id, position) DEFERRABLE INITIALLY IMMEDIATE
);

CREATE TABLE IF NOT EXISTS plus_enrollments (
    id              BIGSERIAL PRIMARY KEY,
    uuid            UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    sequence_id     INTEGER NOT NULL REFERENCES plus_sequences(id) ON DELETE CASCADE ON UPDATE CASCADE,
    subscriber_id   INTEGER NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE ON UPDATE CASCADE,
    source          TEXT NOT NULL CHECK (source IN ('trigger', 'manual', 'manual-bulk', 'api')),
    status          TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'completed', 'cancelled')),
    next_step       INTEGER NOT NULL DEFAULT 1,
    next_send_at    TIMESTAMP WITH TIME ZONE NULL,
    cancel_reason   TEXT NULL,
    enrolled_at     TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
-- At most one active pass per person per sequence (history of past passes is kept).
CREATE UNIQUE INDEX IF NOT EXISTS idx_plus_enrollments_one_active
    ON plus_enrollments (sequence_id, subscriber_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_plus_enrollments_due
    ON plus_enrollments (next_send_at) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_plus_enrollments_sub ON plus_enrollments (subscriber_id);

CREATE TABLE IF NOT EXISTS plus_sends (
    id               BIGSERIAL PRIMARY KEY,
    -- Used in the "send me the next email now" link.
    uuid             UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    enrollment_id    BIGINT NOT NULL REFERENCES plus_enrollments(id) ON DELETE CASCADE ON UPDATE CASCADE,
    step_id          INTEGER NULL REFERENCES plus_sequence_steps(id) ON DELETE SET NULL ON UPDATE CASCADE,
    subscriber_id    INTEGER NOT NULL,
    status           TEXT NOT NULL DEFAULT 'queued'
                         CHECK (status IN ('queued', 'failed', 'skipped')),
    error            TEXT NULL,
    attempts         INTEGER NOT NULL DEFAULT 0,
    skip_clicked_at  TIMESTAMP WITH TIME ZONE NULL,
    created_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    -- Never send the same step twice in one pass.
    UNIQUE (enrollment_id, step_id)
);
CREATE INDEX IF NOT EXISTS idx_plus_sends_sub ON plus_sends (subscriber_id);
