package execenv

import (
	"strings"
	"testing"
)

func TestWriteAlwaysUseCLIForbidsDiskSearch(t *testing.T) {
	var b strings.Builder
	writeAlwaysUseCLI(&b)
	out := b.String()
	if !strings.Contains(out, "## Important: Always Use the `multica` CLI") {
		t.Fatalf("missing Always Use CLI heading:\n%s", out)
	}
	if !strings.Contains(out, "missionos") {
		t.Errorf("brief should mention missionos as the same binary:\n%s", out)
	}
	if !strings.Contains(out, "do not search the disk") {
		t.Errorf("brief should forbid disk search:\n%s", out)
	}
}
