-- listmonk-plus migration 0006: a sequence-wide default landing page for the
-- "send me the next email now" button. A step's own skip_landing_url wins; with
-- neither, the built-in confirmation page is shown.
ALTER TABLE plus_sequences ADD COLUMN IF NOT EXISTS default_skip_landing_url TEXT NULL;
