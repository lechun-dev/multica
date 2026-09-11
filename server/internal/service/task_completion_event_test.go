package service

import (
	"strings"
	"testing"
)

func TestTaskCompletedFieldsIncludesRedactedFinalOutput(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 24)
	fields := taskCompletedFields([]byte(`{"task_id":"task-1","output":"第一行\\n第二行 ` + secret + `"}`))

	output, ok := fields["output"].(string)
	if !ok {
		t.Fatalf("completion output type = %T, want string", fields["output"])
	}
	if !strings.Contains(output, "第一行\n第二行") {
		t.Fatalf("completion output did not decode newlines: %q", output)
	}
	if strings.Contains(output, secret) || !strings.Contains(output, "[REDACTED API KEY]") {
		t.Fatalf("completion output was not redacted: %q", output)
	}
}

func TestTaskCompletedFieldsOmitsEmptyOrMalformedOutput(t *testing.T) {
	for _, result := range [][]byte{[]byte(`{"task_id":"task-1","output":"  "}`), []byte(`not-json`)} {
		if fields := taskCompletedFields(result); len(fields) != 0 {
			t.Fatalf("completion fields = %+v, want empty", fields)
		}
	}
}
