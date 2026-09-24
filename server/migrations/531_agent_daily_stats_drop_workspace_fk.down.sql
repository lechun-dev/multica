-- Restores the foreign key. Only correct while no workspace row is locked FOR
-- UPDATE for the duration of a task status write; re-adding it reintroduces that
-- stall, so this exists for symmetry rather than as a recommended rollback.
ALTER TABLE agent_daily_stats
    ADD CONSTRAINT agent_daily_stats_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;
