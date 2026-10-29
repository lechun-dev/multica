package metrics

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/wscompress"
	"github.com/prometheus/client_golang/prometheus"
)

func TestWSCompressionHistogramDuringConcurrentWrites(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(NewWSCompressionCollector())
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		s := wscompress.Stats(wscompress.Realtime)
		for {
			select {
			case <-done:
				return
			default:
			}
			s.WriteNanos.Add(1)
			for i := range wscompress.WriteBounds {
				s.WriteBuckets[i].Add(1)
			}
			s.WriteAttempts.Add(1)
		}
	}()
	defer func() { close(done); <-stopped }()
	for range 500 {
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			for _, metric := range family.Metric {
				if h := metric.Histogram; h != nil {
					var previous uint64
					for _, bucket := range h.Bucket {
						if bucket.GetCumulativeCount() < previous || bucket.GetCumulativeCount() > h.GetSampleCount() {
							t.Fatal("invalid concurrent histogram")
						}
						previous = bucket.GetCumulativeCount()
					}
				}
			}
		}
	}
}

func TestWSCompressionMetricsHaveOnlyFixedEndpointLabels(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(NewWSCompressionCollector())
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, family := range families {
		name := family.GetName()
		found[name] = true
		if !strings.HasPrefix(name, "multica_ws_compression_") {
			t.Fatalf("unexpected metric %q", name)
		}
		if name == "multica_ws_compression_reload_errors_total" {
			if len(family.Metric) != 1 || len(family.Metric[0].Label) != 0 {
				t.Fatal("reload metric leaked labels")
			}
			continue
		}
		endpoints := map[string]bool{}
		for _, metric := range family.Metric {
			if len(metric.Label) != 1 || metric.Label[0].GetName() != "endpoint" {
				t.Fatalf("unexpected labels on %s", name)
			}
			endpoints[metric.Label[0].GetValue()] = true
			if h := metric.Histogram; h != nil {
				var previous uint64
				for _, bucket := range h.Bucket {
					if bucket.GetCumulativeCount() < previous || bucket.GetCumulativeCount() > h.GetSampleCount() {
						t.Fatal("invalid histogram")
					}
					previous = bucket.GetCumulativeCount()
				}
			}
		}
		if len(family.Metric) != 2 || !endpoints["realtime"] || !endpoints["daemon"] {
			t.Fatalf("unexpected endpoint labels on %s", name)
		}
	}
	for _, name := range []string{"original_bytes_total", "sent_bytes_total", "compressed_original_bytes_total", "write_seconds", "inbound_too_large_total", "reload_errors_total"} {
		if !found["multica_ws_compression_"+name] {
			t.Fatalf("missing metric %s", name)
		}
	}
}
