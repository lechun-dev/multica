BEGIN;

LOCK TABLE agent_task_queue IN SHARE ROW EXCLUSIVE MODE;

DROP TRIGGER IF EXISTS trg_agent_daily_stats_for_queue ON agent_task_queue;
DROP FUNCTION IF EXISTS maintain_agent_daily_stats_for_queue();
DROP FUNCTION IF EXISTS agent_daily_stats_apply(UUID, UUID, DATE, BIGINT, BIGINT, BIGINT, BIGINT, BIGINT);

ALTER TABLE agent_daily_stats
    DROP COLUMN cancelled_count,
    DROP COLUMN completed_count;

CREATE FUNCTION agent_daily_stats_apply(
    p_workspace_id UUID,
    p_agent_id UUID,
    p_stat_date DATE,
    p_run_delta BIGINT,
    p_task_delta BIGINT,
    p_failed_delta BIGINT
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_workspace_id IS NULL OR p_agent_id IS NULL OR p_stat_date IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO agent_daily_stats (
        workspace_id, agent_id, stat_date,
        run_count, task_count, failed_count
    )
    VALUES (
        p_workspace_id, p_agent_id, p_stat_date,
        GREATEST(p_run_delta, 0),
        GREATEST(p_task_delta, 0),
        GREATEST(p_failed_delta, 0)
    )
    ON CONFLICT (workspace_id, agent_id, stat_date) DO UPDATE
    SET run_count = GREATEST(0, agent_daily_stats.run_count + EXCLUDED.run_count + LEAST(p_run_delta, 0)),
        task_count = GREATEST(0, agent_daily_stats.task_count + EXCLUDED.task_count + LEAST(p_task_delta, 0)),
        failed_count = GREATEST(0, agent_daily_stats.failed_count + EXCLUDED.failed_count + LEAST(p_failed_delta, 0));
END;
$$;

CREATE FUNCTION maintain_agent_daily_stats_for_queue()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    old_workspace_id UUID;
    new_workspace_id UUID;
    old_run_date DATE;
    new_run_date DATE;
    old_completion_date DATE;
    new_completion_date DATE;
    old_task_delta BIGINT;
    new_task_delta BIGINT;
    old_failed_delta BIGINT;
    new_failed_delta BIGINT;
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        SELECT workspace_id INTO old_workspace_id FROM agent WHERE id = OLD.agent_id;
        old_run_date := (OLD.created_at AT TIME ZONE 'UTC')::date;
        old_completion_date := CASE WHEN OLD.completed_at IS NULL THEN NULL ELSE (OLD.completed_at AT TIME ZONE 'UTC')::date END;
        old_task_delta := CASE WHEN OLD.completed_at IS NULL THEN 0 ELSE 1 END;
        old_failed_delta := CASE WHEN OLD.completed_at IS NOT NULL AND OLD.status = 'failed' THEN 1 ELSE 0 END;

        PERFORM agent_daily_stats_apply(old_workspace_id, OLD.agent_id, old_run_date, -1, 0, 0);
        PERFORM agent_daily_stats_apply(old_workspace_id, OLD.agent_id, old_completion_date, 0, -old_task_delta, -old_failed_delta);
    END IF;

    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        SELECT workspace_id INTO new_workspace_id FROM agent WHERE id = NEW.agent_id;
        new_run_date := (NEW.created_at AT TIME ZONE 'UTC')::date;
        new_completion_date := CASE WHEN NEW.completed_at IS NULL THEN NULL ELSE (NEW.completed_at AT TIME ZONE 'UTC')::date END;
        new_task_delta := CASE WHEN NEW.completed_at IS NULL THEN 0 ELSE 1 END;
        new_failed_delta := CASE WHEN NEW.completed_at IS NOT NULL AND NEW.status = 'failed' THEN 1 ELSE 0 END;

        PERFORM agent_daily_stats_apply(new_workspace_id, NEW.agent_id, new_run_date, 1, 0, 0);
        PERFORM agent_daily_stats_apply(new_workspace_id, NEW.agent_id, new_completion_date, 0, new_task_delta, new_failed_delta);
        RETURN NEW;
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER trg_agent_daily_stats_for_queue
AFTER INSERT OR UPDATE OF agent_id, created_at, completed_at, status OR DELETE
ON agent_task_queue
FOR EACH ROW EXECUTE FUNCTION maintain_agent_daily_stats_for_queue();

COMMIT;
