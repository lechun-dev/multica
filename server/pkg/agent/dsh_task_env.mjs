// 2026-10-09 coder(lq): The official registry rebuilds DSH_* facts for Bash and PowerShell; credentials never enter config or prompts.
export const inject = ['shellEnv'];

export function apply(ctx, expected) {
  const keys = ['MULTICA_TASK_ID', 'MULTICA_AGENT_ID', 'MULTICA_WORKSPACE_ID', 'MULTICA_TASK_CONFIG_ROOT'];
  const snapshot = {};
  for (const key of keys) {
    const value = process.env[key];
    if (!value || value !== expected?.[key]) {
      throw new Error('MissionOS DSH task identity mismatch');
    }
    snapshot[`DSH_${key}`] = value;
  }
  const token = process.env.MULTICA_TOKEN;
  if (!token || !/^mat_[^\s]+$/.test(token)) {
    throw new Error('MissionOS DSH requires a task-scoped credential');
  }
  snapshot.DSH_MULTICA_TOKEN = token;
  Object.freeze(snapshot);
  ctx.shellEnv.register({
    name: 'missionos-task-env',
    variables: Object.fromEntries(Object.keys(snapshot).map(key => [key, {
      description: 'MissionOS current task environment; not a human login credential.',
    }])),
    resolve: execution => execution.agent === undefined ? {} : snapshot,
  });
}
