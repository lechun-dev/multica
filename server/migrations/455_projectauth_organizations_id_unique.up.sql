-- 2026-09-01 coder(lq): Keep synchronized organization identifiers unique so
-- provider-neutral directory rows can be referenced unambiguously by grants.
CREATE UNIQUE INDEX CONCURRENTLY projectauth_organizations_id_uniq
    ON projectauth_organizations (id);
