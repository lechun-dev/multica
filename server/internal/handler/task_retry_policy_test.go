package handler

import "testing"

// 2026-09-03 coder(lq): Keep PATCH omission semantics covered independently
// from the database-backed handler tests.
func TestParseTaskRetryPolicyInputPartialFields(t *testing.T) {
	name := "provider network"
	enabled := false
	req := taskRetryPolicyRequest{Name: &name, Enabled: &enabled}

	input, err := parseTaskRetryPolicyInput(req, true)
	if err != nil {
		t.Fatalf("parse partial policy: %v", err)
	}
	if input.Name != name || input.Enabled {
		t.Fatalf("parsed partial values = name %q enabled %v", input.Name, input.Enabled)
	}
	if input.MatchType != "failure_reason" || input.MatchValue != "" {
		t.Fatalf("omitted match fields were not left unset: type=%q value=%q", input.MatchType, input.MatchValue)
	}
}

func TestParseTaskRetryPolicyInputCreateRequiresMatchFields(t *testing.T) {
	name := "missing match"
	_, err := parseTaskRetryPolicyInput(taskRetryPolicyRequest{Name: &name}, false)
	if err == nil {
		t.Fatal("create accepted a policy without match fields")
	}
}

func TestParseTaskRetryPolicyInputAcceptsExplicitDefaultMatchType(t *testing.T) {
	name := "explicit default"
	matchType := "failure_reason"
	matchValue := "timeout"
	_, err := parseTaskRetryPolicyInput(taskRetryPolicyRequest{
		Name:       &name,
		MatchType:  &matchType,
		MatchValue: &matchValue,
	}, true)
	if err != nil {
		t.Fatalf("explicit default match type was rejected: %v", err)
	}
}
