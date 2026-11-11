-- listmonk-plus migration 0004: sequence steps are campaigns (plus/docs/003).
--
-- Each step now owns a campaign (always 'draft'; the worker sends it to one
-- subscriber at a time). Transactional-template steps are removed.
--
-- DESTRUCTIVE for sequence progress: existing steps, enrollments and sends are
-- deleted, and active sequences are paused (they have no steps left). This is
-- safe because, at the time of writing, sequences built with transactional
-- templates only ever existed as development test data (decision M5-D2).
-- Sequences themselves (name, list, rules) are kept.

TRUNCATE plus_sends, plus_enrollments, plus_sequence_steps;
UPDATE plus_sequences SET status = 'paused', updated_at = NOW() WHERE status = 'active';

ALTER TABLE plus_sequence_steps DROP COLUMN IF EXISTS template_id;
ALTER TABLE plus_sequence_steps DROP COLUMN IF EXISTS subject;

-- RESTRICT: a step's campaign can't be deleted while the step exists (a DB-level
-- guard in addition to the API guards); deleting a step deletes its campaign.
ALTER TABLE plus_sequence_steps
    ADD COLUMN campaign_id INTEGER NOT NULL UNIQUE
        REFERENCES campaigns(id) ON DELETE RESTRICT ON UPDATE CASCADE;
