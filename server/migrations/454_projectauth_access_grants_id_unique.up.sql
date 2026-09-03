-- 2026-09-01 coder(lq): Keep generated grant identifiers unique without
-- blocking readers while the authorization tables are adopted in production.
CREATE UNIQUE INDEX CONCURRENTLY projectauth_access_grants_id_uniq
    ON projectauth_access_grants (id);
