package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderSessionStoresIsolateIssueThreads(t *testing.T) {
	for _, provider := range []struct {
		name string
		key  func(TaskContextForEnv) string
	}{
		{"codex", func(task TaskContextForEnv) string { return codexSessionStoreKey("staging", task) }},
		{"hermes", hermesConversationSegment},
	} {
		t.Run(provider.name, func(t *testing.T) {
			a := TaskContextForEnv{AgentID: "agent", IssueID: "issue", TriggerThreadID: "thread-a", TriggerCommentID: "comment-a"}
			b := a
			b.TriggerThreadID = "thread-b"
			if provider.key(a) == provider.key(b) {
				t.Fatal("independent issue threads share a mutable session store")
			}
			followup := a
			followup.TriggerCommentID = "reply-a"
			if provider.key(a) != provider.key(followup) {
				t.Fatal("same-thread follow-up lost its conversation store")
			}
			unthreaded := a
			unthreaded.TriggerThreadID = ""
			unthreaded.TriggerCommentID = ""
			if provider.key(a) == provider.key(unthreaded) {
				t.Fatal("thread inherited the unthreaded issue store")
			}
		})
	}
}

func TestCodexIssueThreadSessionMountsAreIsolated(t *testing.T) {
	requireSymlinks(t)
	t.Parallel()
	sharedHome := t.TempDir()
	a := TaskContextForEnv{AgentID: "agent", IssueID: "issue", TriggerThreadID: "thread-a"}
	b := a
	b.TriggerThreadID = "thread-b"
	prepare := func(task TaskContextForEnv) string {
		t.Helper()
		home := t.TempDir()
		key := codexSessionStoreKey("staging", task)
		if err := prepareCodexSessionsDir(home, sharedHome, CodexHomeOptions{
			IsLocalDirectory: true, SessionStoreKey: key,
		}, testLogger()); err != nil {
			t.Fatalf("prepare thread %s: %v", task.TriggerThreadID, err)
		}
		assertSessionsLinkedToStore(t, filepath.Join(home, "sessions"), codexSessionStoreDir(sharedHome, key))
		return home
	}
	homeA := prepare(a)
	const session = "thread-a-session"
	seedFakeRollout(t, filepath.Join(homeA, "sessions"), "2026", "11", "05", session, 32)
	if CodexResumeRolloutPresent(prepare(b), session) {
		t.Fatal("thread B can read thread A's rollout")
	}
	if !CodexResumeRolloutPresent(prepare(a), session) {
		t.Fatal("thread A follow-up lost its rollout")
	}
}

func TestHermesIssueThreadSessionMountsAreIsolated(t *testing.T) {
	requireSymlinks(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sharedHome := t.TempDir()
	a := TaskContextForEnv{AgentID: "agent", IssueID: "issue", TriggerThreadID: "thread-a"}
	b := a
	b.TriggerThreadID = "thread-b"
	prepare := func(task TaskContextForEnv) string {
		t.Helper()
		home := t.TempDir()
		store := HermesSessionStorePath("staging", task.AgentID, "", task)
		mount, err := prepareHermesHome(home, sharedHome, false,
			[]SkillContextForEnv{{Name: "test", Content: "# Test"}}, nil, "", store, testLogger())
		if err != nil || !mount.Mounted {
			t.Fatalf("prepare thread %s: mount=%+v err=%v", task.TriggerThreadID, mount, err)
		}
		return home
	}
	homeA := prepare(a)
	mustWrite(t, filepath.Join(homeA, "state.db"), "thread A transcript")
	if _, err := os.ReadFile(filepath.Join(prepare(b), "state.db")); !os.IsNotExist(err) {
		t.Fatalf("thread B can read thread A's transcript (err=%v)", err)
	}
	if got, err := os.ReadFile(filepath.Join(prepare(a), "state.db")); err != nil || string(got) != "thread A transcript" {
		t.Fatalf("thread A follow-up lost its transcript: %q, %v", got, err)
	}
}
