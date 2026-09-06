export type TaskRetryPolicyMatchType =
  | "failure_reason"
  | "http_status"
  | "error_contains";

export interface TaskRetryPolicy {
  id: string;
  workspace_id: string;
  name: string;
  enabled: boolean;
  priority: number;
  match_type: TaskRetryPolicyMatchType;
  match_value: string;
  max_attempts: number;
  delay_schedule: number[];
  created_at: string;
  updated_at: string;
}

export interface TaskRetryPolicyRequest {
  name: string;
  enabled?: boolean;
  priority?: number;
  match_type: TaskRetryPolicyMatchType;
  match_value: string;
  max_attempts?: number;
  delay_schedule?: number[];
}

export type UpdateTaskRetryPolicyRequest = Partial<TaskRetryPolicyRequest>;
