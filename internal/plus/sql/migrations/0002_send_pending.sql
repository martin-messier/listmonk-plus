-- listmonk-plus migration 0002: add the 'pending' send status.
--
-- A send row is created as 'pending' just before the email is handed to the
-- mailer and becomes 'queued' right after. A row found still 'pending' later
-- means the process stopped in between; the step is then treated as sent
-- (at-most-once delivery: never risk sending the same email twice).
ALTER TABLE plus_sends DROP CONSTRAINT IF EXISTS plus_sends_status_check;
ALTER TABLE plus_sends ADD CONSTRAINT plus_sends_status_check
    CHECK (status IN ('pending', 'queued', 'failed', 'skipped'));
ALTER TABLE plus_sends ALTER COLUMN status SET DEFAULT 'pending';
