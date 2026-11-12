-- name: get-dashboard-charts
SELECT data FROM mat_dashboard_charts;

-- name: get-dashboard-counts
-- listmonk-plus: sequence step campaigns (and their messages) are only shown under
-- Sequences, so they're subtracted from the campaign counts here rather than by
-- changing mat_dashboard_counts (which upstream migrations recreate).
WITH st AS (
    SELECT COUNT(*) AS n, COALESCE(SUM(c.sent), 0) AS sent
    FROM plus_sequence_steps ps JOIN campaigns c ON c.id = ps.campaign_id
)
SELECT CASE WHEN st.n = 0 THEN data::JSONB ELSE
    JSONB_SET(JSONB_SET(JSONB_SET(data::JSONB,
        '{campaigns,total}', TO_JSONB(GREATEST(0, COALESCE((data->'campaigns'->>'total')::INT, 0) - st.n))),
        '{campaigns,by_status,draft}', TO_JSONB(GREATEST(0, COALESCE((data->'campaigns'->'by_status'->>'draft')::INT, 0) - st.n))),
        '{messages}', TO_JSONB(GREATEST(0, COALESCE((data->>'messages')::BIGINT, 0) - st.sent)))
    END AS data
FROM mat_dashboard_counts, st;

-- name: get-settings
SELECT JSON_OBJECT_AGG(key, value) AS settings FROM (SELECT * FROM settings ORDER BY key) t;

-- name: update-settings
UPDATE settings AS s SET value = c.value
    -- For each key in the incoming JSON map, update the row with the key and its value.
    FROM(SELECT * FROM JSONB_EACH($1)) AS c(key, value) WHERE s.key = c.key;

-- name: update-settings-by-key
UPDATE settings SET value = $2, updated_at = NOW() WHERE key = $1;

-- name: get-db-info
SELECT JSON_BUILD_OBJECT('version', (SELECT VERSION()),
                        'size_mb', (SELECT ROUND(pg_database_size((SELECT CURRENT_DATABASE()))/(1024^2)))) AS info;
