package execenv

// taskConversationSegment keeps each independent issue thread's mutable
// provider history separate. A flat segment preserves session-store GC's
// conversation-level reservation contract. Unthreaded issues and chats retain
// their existing paths.
func taskConversationSegment(task TaskContextForEnv) string {
	if issue := sanitizePathSegment(task.IssueID); issue != "" {
		if thread := sanitizePathSegment(task.TriggerThreadID); thread != "" {
			return issue + "__thread_" + thread
		}
		return issue
	}
	if chat := sanitizePathSegment(task.ChatSessionID); chat != "" {
		return "chat_" + chat
	}
	return ""
}
