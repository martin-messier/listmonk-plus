-- listmonk-plus migration 0003: grant the sequences permissions to the Super Admin role.
--
-- The Super Admin role (id 1) stores the permissions that existed when it was
-- created. Admin templates check stored permissions (the API's permission
-- middleware short-circuits for Super Admins, templates don't), so new
-- permissions must be added explicitly, as upstream's own migrations do
-- (see internal/migrations/v5.0.0.go).
UPDATE roles SET permissions = permissions || '{sequences:get}'
    WHERE id = 1 AND NOT permissions @> '{sequences:get}';
UPDATE roles SET permissions = permissions || '{sequences:manage}'
    WHERE id = 1 AND NOT permissions @> '{sequences:manage}';
