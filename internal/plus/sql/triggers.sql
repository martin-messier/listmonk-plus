-- listmonk-plus: triggers on upstream tables that feed plus_events.
--
-- This file is (re)applied on EVERY startup and must stay idempotent. Triggers
-- live on upstream tables, which an upstream reinstall (`--install`) drops and
-- recreates; re-applying on startup guarantees they always exist.
--
-- Statement-level triggers with transition tables are used instead of row-level
-- triggers so that bulk operations (CSV import, add-to-lists-by-query on
-- 100k subscribers) cost one INSERT ... SELECT per statement, not one per row.
-- Postgres requires one trigger per event when transition tables are used.
--
-- Events emitted (see plus/docs/001-sequences-design.md §3.1):
--   subscription.created       inserted as unconfirmed/confirmed, or re-subscribed
--                              (unsubscribed -> unconfirmed/confirmed)
--   subscription.confirmed     inserted as, or changed to, confirmed
--   subscription.unsubscribed  changed to unsubscribed
--   subscription.removed       row deleted (removed from list, or subscriber deleted)

CREATE OR REPLACE FUNCTION plus_sub_lists_on_insert() RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO plus_events (type, subscriber_id, list_id, payload)
    SELECT e.type, n.subscriber_id, n.list_id,
           jsonb_build_object('old_status', NULL, 'new_status', n.status, 'resubscribe', false)
    FROM new_rows n
    CROSS JOIN LATERAL (
        SELECT 'subscription.created' AS type, 1 AS ord WHERE n.status IN ('unconfirmed', 'confirmed')
        UNION ALL
        SELECT 'subscription.confirmed', 2 WHERE n.status = 'confirmed'
    ) e
    ORDER BY n.subscriber_id, n.list_id, e.ord;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION plus_sub_lists_on_update() RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO plus_events (type, subscriber_id, list_id, payload)
    SELECT e.type, n.subscriber_id, n.list_id,
           jsonb_build_object('old_status', o.status, 'new_status', n.status,
                              'resubscribe', o.status = 'unsubscribed')
    FROM new_rows n
    JOIN old_rows o ON o.subscriber_id = n.subscriber_id AND o.list_id = n.list_id
    CROSS JOIN LATERAL (
        SELECT 'subscription.created' AS type, 1 AS ord
            WHERE o.status = 'unsubscribed' AND n.status IN ('unconfirmed', 'confirmed')
        UNION ALL
        SELECT 'subscription.confirmed', 2
            WHERE n.status = 'confirmed' AND o.status <> 'confirmed'
        UNION ALL
        SELECT 'subscription.unsubscribed', 3
            WHERE n.status = 'unsubscribed' AND o.status <> 'unsubscribed'
    ) e
    WHERE o.status IS DISTINCT FROM n.status
    ORDER BY n.subscriber_id, n.list_id, e.ord;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION plus_sub_lists_on_delete() RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO plus_events (type, subscriber_id, list_id, payload)
    SELECT 'subscription.removed', o.subscriber_id, o.list_id,
           jsonb_build_object('old_status', o.status)
    FROM old_rows o
    ORDER BY o.subscriber_id, o.list_id;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS plus_sub_lists_insert ON subscriber_lists;
CREATE TRIGGER plus_sub_lists_insert
    AFTER INSERT ON subscriber_lists
    REFERENCING NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION plus_sub_lists_on_insert();

DROP TRIGGER IF EXISTS plus_sub_lists_update ON subscriber_lists;
CREATE TRIGGER plus_sub_lists_update
    AFTER UPDATE ON subscriber_lists
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION plus_sub_lists_on_update();

DROP TRIGGER IF EXISTS plus_sub_lists_delete ON subscriber_lists;
CREATE TRIGGER plus_sub_lists_delete
    AFTER DELETE ON subscriber_lists
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT EXECUTE FUNCTION plus_sub_lists_on_delete();
