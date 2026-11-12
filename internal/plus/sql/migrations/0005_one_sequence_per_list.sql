-- listmonk-plus migration 0005: one sequence per list, whatever its status or trigger
-- (decision D5, clarified 2026-11-11). Replaces 0001's "one automated sequence per
-- list" rule, which still allowed extra manual-only sequences on the same list.
--
-- Fails (and Listmonk won't start) if a list already has several sequences: those
-- must be resolved by hand first, since there's no safe automatic choice.
DO $$
DECLARE dup TEXT;
BEGIN
    SELECT string_agg(l.name || ' (' || n || ' sequences)', ', ') INTO dup
    FROM (SELECT list_id, COUNT(*) AS n FROM plus_sequences GROUP BY list_id HAVING COUNT(*) > 1) d
    JOIN lists l ON l.id = d.list_id;
    IF dup IS NOT NULL THEN
        RAISE EXCEPTION 'listmonk-plus 0005: these lists have more than one sequence: %. Keep one sequence per list (delete or move the others), then restart.', dup;
    END IF;
END $$;

DROP INDEX IF EXISTS idx_plus_sequences_one_auto_per_list;
CREATE UNIQUE INDEX IF NOT EXISTS idx_plus_sequences_one_per_list ON plus_sequences (list_id);
