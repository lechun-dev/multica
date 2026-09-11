package main

import (
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/dingtalkpersonal"
)

func TestDWSUnavailableDetailsPreservesSpecificConfigurationFailure(t *testing.T) {
	code, message := dwsUnavailableDetails(&dingtalkpersonal.DeliveryError{
		Code:    "dws_oauth_not_configured",
		Message: "missing OAuth credentials",
	})
	if code != "dws_oauth_not_configured" || message != "missing OAuth credentials" {
		t.Fatalf("details = (%q, %q)", code, message)
	}
}

func TestDWSUnavailableDetailsDoesNotExposeInternalErrors(t *testing.T) {
	code, message := dwsUnavailableDetails(errors.New("database password leaked here"))
	if code != "dws_server_not_configured" {
		t.Fatalf("code = %q", code)
	}
	if message == "database password leaked here" {
		t.Fatal("internal error was exposed")
	}
}
