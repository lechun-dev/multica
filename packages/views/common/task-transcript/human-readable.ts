import type {
  TraceCallStep,
  TraceMessageStep,
  TraceRow,
} from "./build-steps";
import { traceToolArgSummary } from "./trace-event-presenter";

/**
 * 2026-08-28 coder(lq): Keep the plain-language view as a data-only adapter;
 * the transcript UI can change without changing how execution events are
 * interpreted.
 */
export type HumanReadableAction = "command" | "read" | "search" | "edit" | "tool";

export interface HumanReadableStep {
  seq: number;
  kind: "text" | "thinking" | "action" | "error" | "group";
  /** Agent prose, thinking text, or an error body. */
  text?: string;
  action?: HumanReadableAction;
  subject?: string;
  tool?: string;
  completed?: boolean;
  count?: number;
}

function textOf(step: TraceMessageStep): string | undefined {
  const text = step.item.content?.trim();
  return text && text.length > 0 ? text : undefined;
}

function callDescription(step: TraceCallStep): Omit<HumanReadableStep, "seq" | "kind"> {
  const input = step.call?.input;
  const subject = traceToolArgSummary(input) || step.tool || undefined;

  if (typeof input?.command === "string") {
    return { action: "command", subject, tool: step.tool, completed: !!step.result };
  }
  if (
    typeof input?.old_string === "string" ||
    typeof input?.content === "string" ||
    input?.changes !== undefined
  ) {
    return { action: "edit", subject, tool: step.tool, completed: !!step.result };
  }
  if (typeof input?.query === "string" || typeof input?.pattern === "string") {
    return { action: "search", subject, tool: step.tool, completed: !!step.result };
  }
  if (typeof input?.file_path === "string" || typeof input?.path === "string") {
    return { action: "read", subject, tool: step.tool, completed: !!step.result };
  }
  return { action: "tool", subject, tool: step.tool, completed: !!step.result };
}

function humanizeCall(step: TraceCallStep): HumanReadableStep {
  return { seq: step.seq, kind: "action", ...callDescription(step) };
}

function humanizeMessage(step: TraceMessageStep): HumanReadableStep {
  return {
    seq: step.seq,
    kind: step.kind === "error" ? "error" : step.kind,
    text: textOf(step),
  };
}

/** Convert one technical transcript row into a stable, localizable model. */
export function humanizeTraceRow(row: TraceRow): HumanReadableStep {
  if (row.kind === "group") {
    const first = row.steps[0];
    const description = first ? callDescription(first) : { action: "tool" as const };
    return {
      seq: row.seq,
      kind: "group",
      ...description,
      tool: row.tool,
      count: row.steps.length,
      completed: row.steps.every((step) => !!step.result),
    };
  }
  return row.kind === "call" ? humanizeCall(row) : humanizeMessage(row);
}

/** Build the human-readable list while retaining every technical row. */
export function humanizeTraceRows(rows: TraceRow[]): HumanReadableStep[] {
  return rows.map(humanizeTraceRow);
}
