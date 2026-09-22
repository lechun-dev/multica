package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Every collector has to be registrable together. A duplicate name or a
// malformed help string only surfaces at MustRegister, which in production is
// process start.
func TestWecomMetricsRegisterCleanly(t *testing.T) {
	reg := prometheus.NewRegistry()
	for _, c := range NewWecomMetrics().Collectors() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
}

// The adapter's Metrics interface and this implementation must not drift. The
// compile-time check lives in the adapter; this one catches a method that
// exists but does nothing.
func TestEveryWecomCounterActuallyCounts(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWecomMetrics()
	for _, c := range m.Collectors() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register: %v", err)
		}
	}

	m.RecordConnectFailure()
	m.RecordAuthFailure()
	m.RecordCallbackQueued()
	m.RecordCallbackQueueBlocked()

	seen := gatherWecomValues(t, reg)
	for _, want := range []string{
		"multica_wecom_connect_failures_total",
		"multica_wecom_auth_failures_total",
		"multica_wecom_inbound_callbacks_total",
		"multica_wecom_inbound_queue_blocked_total",
	} {
		if seen[want] != 1 {
			t.Errorf("%s = %v, want 1 — the counter is wired to nothing", want, seen[want])
		}
	}
}

// The whole point of two connection counters is that an operator can tell a
// wrong secret from a dead network. Sharing a series would put them back to
// guessing.
func TestAuthAndConnectFailuresAreSeparateSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWecomMetrics()
	for _, c := range m.Collectors() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register: %v", err)
		}
	}

	m.RecordAuthFailure()
	m.RecordAuthFailure()
	m.RecordConnectFailure()

	seen := gatherWecomValues(t, reg)
	if got := seen["multica_wecom_auth_failures_total"]; got != 2 {
		t.Errorf("auth failures = %v, want 2", got)
	}
	if got := seen["multica_wecom_connect_failures_total"]; got != 1 {
		t.Errorf("connect failures = %v, want 1", got)
	}
}

// No metric here may carry an unbounded identifier as a label — the same rule
// labels.go enforces for the rest of the codebase. installation_id is the one
// that would be tempting to add, and it belongs in the logs instead. Those
// carry it for the two connection failures, which reach the Supervisor as a
// returned error; the two inbound counters have no log line beside them, so
// leaving the label off really does cost the answer to "which bot" on those
// two. That is the trade this test pins, not a free win.
func TestWecomMetricsCarryNoUnboundedLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWecomMetrics()
	for _, c := range m.Collectors() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	m.RecordConnectFailure()
	m.RecordAuthFailure()
	m.RecordCallbackQueued()
	m.RecordCallbackQueueBlocked()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				if _, forbidden := forbiddenMetricLabels[l.GetName()]; forbidden {
					t.Errorf("%s carries the unbounded label %q", f.GetName(), l.GetName())
				}
				if l.GetName() == "installation_id" {
					t.Errorf("%s labels by installation_id; that belongs in the logs", f.GetName())
				}
			}
		}
	}
}

// The registry is what production actually scrapes; a collector that is built
// but never registered exports nothing.
func TestTheRegistryExposesTheWecomCounters(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	if r.Wecom == nil {
		t.Fatal("Registry.Wecom is nil; nothing can report through it")
	}
	r.Wecom.RecordAuthFailure()

	families, err := r.Gatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == "multica_wecom_auth_failures_total" {
			return
		}
	}
	t.Fatal("multica_wecom_auth_failures_total is not on the registry the metrics server scrapes")
}

func gatherWecomValues(t *testing.T, reg prometheus.Gatherer) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]float64{}
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			out[f.GetName()] = wecomValueOf(metric)
		}
	}
	return out
}

func wecomValueOf(m *dto.Metric) float64 {
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	if g := m.GetGauge(); g != nil {
		return g.GetValue()
	}
	return 0
}

// TestWecomOutboundMetricsAreExported covers the EXPORTED contract, not the
// adapter's test double: the names Prometheus will scrape, the reason label,
// and that every collector is actually registered. A dashboard reads these
// strings; a rename that only the double knows about is a silent outage of
// whatever alert watches them.
func TestWecomOutboundMetricsAreExported(t *testing.T) {
	m := NewWecomMetrics()
	reg := prometheus.NewRegistry()
	for _, c := range m.Collectors() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register: %v", err)
		}
	}

	m.RecordOutboundDelivered()
	m.RecordOutboundDropped("no_live_connection")
	m.RecordOutboundSkipped("origin_not_channel")
	m.RecordAttachmentDelivered()
	m.RecordAttachmentDropped("platform_refused")
	m.RecordAttachmentDeliveryShed()
	m.RecordOutboundUnconfirmed("ack_timeout")
	m.RecordAttachmentUnconfirmed("write_attempted")

	got, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	seen := map[string]bool{}
	labels := map[string]string{}
	for _, mf := range got {
		seen[mf.GetName()] = true
		for _, mm := range mf.GetMetric() {
			for _, l := range mm.GetLabel() {
				labels[mf.GetName()+"/"+l.GetName()] = l.GetValue()
			}
		}
	}
	for _, want := range []string{
		"multica_wecom_outbound_delivered_total",
		"multica_wecom_outbound_dropped_total",
		"multica_wecom_outbound_skipped_total",
		"multica_wecom_outbound_attachment_delivered_total",
		"multica_wecom_outbound_attachment_dropped_total",
		"multica_wecom_outbound_attachment_delivery_shed_total",
		"multica_wecom_outbound_unconfirmed_total",
		"multica_wecom_outbound_attachment_unconfirmed_total",
	} {
		if !seen[want] {
			t.Errorf("%s was not exported", want)
		}
	}
	for name, want := range map[string]string{
		"multica_wecom_outbound_dropped_total/reason":                "no_live_connection",
		"multica_wecom_outbound_skipped_total/reason":                "origin_not_channel",
		"multica_wecom_outbound_attachment_dropped_total/reason":     "platform_refused",
		"multica_wecom_outbound_unconfirmed_total/reason":            "ack_timeout",
		"multica_wecom_outbound_attachment_unconfirmed_total/reason": "write_attempted",
	} {
		if labels[name] != want {
			t.Errorf("%s = %q, want %q", name, labels[name], want)
		}
	}
}

// The Help string is the whole of what an operator is told about a counter: it
// ships on /metrics and it is what a dashboard prints beside the series. So the
// two tests below ask of that text the question an on-call brings at 3am —
// which of these labels means somebody is waiting, and which one do I act on.
//
// Asserting that the labels are all mentioned is not enough, and that is not a
// hypothetical. The list was asserted; the prose around it was then rewritten
// to say the drop/skip boundary is whether a frame went on the wire; the test
// stayed green while the Help pointed the reader away from the alert. What
// actually separates the two counters is the OBLIGATION — was a reply owed to a
// WeCom user — and four of the five drop reasons are settled before anything is
// written to a socket. So these assert where each label sits relative to that
// line, not that it appears.
//
// The words are load-bearing. Reword the Help and this goes red; keep the claim
// and the test moves with it.
func TestSkippedHelpPutsEachReasonOnTheRightSideOfTheAlertLine(t *testing.T) {
	help := helpFor(t, "multica_wecom_outbound_skipped_total", func(m *WecomMetrics) {
		m.RecordOutboundSkipped("no_delivery_row")
	})
	// Four that were never owed to WeCom, the one that may well have been, and
	// (below) the one nothing here can say either way about.
	ordinary := []string{
		"origin_not_channel", "not_wecom_turn", "installation_inactive", "nothing_to_say",
	}
	const actionable = "no_delivery_row"

	alert := sentenceNaming(t, help, actionable)
	if !containsAny(alert, "may well be owed", "may be owed") {
		t.Errorf("the %q sentence does not say a reply may be owed, which is the whole reason it is "+
			"not filed with the harmless four:\n%s", actionable, alert)
	}
	if !strings.Contains(alert, "alert") {
		t.Errorf("the %q sentence does not tell the operator this is the label to alert on:\n%s",
			actionable, alert)
	}
	for _, reason := range ordinary {
		if strings.Contains(alert, reason) {
			t.Errorf("%q shares a sentence with %q, so the Help reads as though the alert covers it "+
				"too:\n%s", reason, actionable, alert)
		}
		line := sentenceNaming(t, help, reason)
		if !containsAny(line, "never owed", "not owed") {
			t.Errorf("the %q sentence does not say it was never owed to WeCom — without that, the "+
				"reader has no rule for why these four are not drops:\n%s", reason, line)
		}
	}

	// And the one on neither side. With no route and no batch owner nothing can
	// say whether a reply was owed, so the Help must neither file it with the
	// harmless four ("never owed") nor with the alert.
	const unattributable = "route_unattributable"
	line := sentenceNaming(t, help, unattributable)
	if strings.Contains(alert, unattributable) {
		t.Errorf("%q shares a sentence with %q, so the Help reads as though the alert covers it:\n%s",
			unattributable, actionable, alert)
	}
	if containsAny(line, "never owed", "not owed") {
		t.Errorf("the %q sentence says it was not owed, which is the one thing nothing here can "+
			"establish about it:\n%s", unattributable, line)
	}
	if !strings.Contains(line, "nothing here can say") {
		t.Errorf("the %q sentence does not say the owner is unknowable, so the reader has no rule "+
			"for why it is neither a drop nor an alert:\n%s", unattributable, line)
	}
}

// The dropped counter is the same question read from the other end, so its Help
// has to draw the same line: a drop is a reply somebody was OWED and did not
// get. Not "a frame that went out and failed" — task_missing is decided before
// anything is sent, no_live_connection means there was no socket to send on,
// attachment_not_admitted is refused at admission, and transport_error covers a
// delivery whose budget ran out before its turn on the wire. Four of the five
// never reach WeCom at all. A Help that defines the counter by the wire sends
// an operator hunting a network fault for a reply that was lost on our side of
// it.
func TestDroppedHelpDefinesADropByWhatWasOwed(t *testing.T) {
	help := helpFor(t, "multica_wecom_outbound_dropped_total", func(m *WecomMetrics) {
		m.RecordOutboundDropped("task_missing")
	})
	if first := sentences(help)[0]; !strings.Contains(first, "owed") {
		t.Errorf("the opening sentence does not define a drop by what was owed:\n%s", first)
	}
	if !strings.Contains(help, "outbound_skipped_total") {
		t.Errorf("the help does not say where the completions that were not owed are counted:\n%s", help)
	}
	if containsAny(help, "on the wire", "went out") {
		t.Errorf("the help defines the drop set by whether the frame reached WeCom; four of the five "+
			"reasons never get that far:\n%s", help)
	}
}

// helpFor gathers one counter family and returns its Help. A CounterVec with no
// children is gathered as no family at all, so record has to put a label value
// in before the text is readable.
func helpFor(t *testing.T, name string, record func(*WecomMetrics)) string {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := NewWecomMetrics()
	for _, collector := range m.Collectors() {
		if err := reg.Register(collector); err != nil {
			t.Fatalf("register WeCom collector: %v", err)
		}
	}
	record(m)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather WeCom metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			if help := family.GetHelp(); help != "" {
				return help
			}
			t.Fatalf("%s carries no help text", name)
		}
	}
	t.Fatalf("%s is not exported at all", name)
	return ""
}

func sentences(help string) []string { return strings.Split(help, ". ") }

// sentenceNaming returns the sentence that names reason, and ends the test if
// none does: a label an operator meets in the breakdown and cannot find in the
// Help leaves them with nothing to read.
func sentenceNaming(t *testing.T, help, reason string) string {
	t.Helper()
	for _, sentence := range sentences(help) {
		if strings.Contains(sentence, reason) {
			return sentence
		}
	}
	t.Fatalf("the help does not explain the %q label:\n%s", reason, help)
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
