package metrics

import (
	"github.com/multica-ai/multica/server/internal/wscompress"
	"github.com/prometheus/client_golang/prometheus"
)

type compressionMetric struct {
	desc  *prometheus.Desc
	kind  prometheus.ValueType
	value func(*wscompress.Counters) float64
}

type WSCompressionCollector struct {
	metrics                []compressionMetric
	duration, reloadErrors *prometheus.Desc
}

func NewWSCompressionCollector() *WSCompressionCollector {
	c := &WSCompressionCollector{}
	add := func(name, help string, kind prometheus.ValueType, value func(*wscompress.Counters) float64) {
		c.metrics = append(c.metrics, compressionMetric{prometheus.NewDesc("multica_ws_compression_"+name, help, []string{"endpoint"}, nil), kind, value})
	}
	add("connections_total", "Successful handshakes, including plain connections.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.Connections.Load()) })
	add("negotiated_total", "Connections negotiating permessage-deflate.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.Negotiated.Load()) })
	add("active", "Open observed connections.", prometheus.GaugeValue, func(s *wscompress.Counters) float64 { return float64(s.Active.Load()) })
	add("active_negotiated", "Open connections negotiating permessage-deflate.", prometheus.GaugeValue, func(s *wscompress.Counters) float64 { return float64(s.ActiveNegotiated.Load()) })
	add("original_bytes_total", "Serialized bytes of successful data messages, including plain and skipped messages.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.Original.Load()) })
	add("compressed_original_bytes_total", "Original bytes of successful data messages sent with negotiated compression.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.CompressedOriginal.Load()) })
	add("sent_bytes_total", "Actual frame payload bytes matched to successful data messages; excludes framing and TLS.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.Sent.Load()) })
	add("messages_total", "Successfully sent data messages.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.Messages.Load()) })
	add("compressed_messages_total", "Completed data messages carrying RSV1 on the wire.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.Compressed.Load()) })
	add("write_errors_total", "Failed data writes.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.WriteErrors.Load()) })
	add("transport_payload_bytes_total", "All accepted data frame payload bytes, including partial and pre-auth writes.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.DataPayload.Load()) })
	add("transport_header_bytes_total", "Accepted data frame header bytes.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.DataHeaders.Load()) })
	add("transport_control_bytes_total", "Accepted control frame header and payload bytes.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.ControlBytes.Load()) })
	add("failed_write_bytes_total", "Accepted bytes on network writes returning an error; overlaps transport totals.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.FailedWriteBytes.Load()) })
	add("inbound_too_large_total", "Inbound wire or inflated read limit rejections.", prometheus.CounterValue, func(s *wscompress.Counters) float64 { return float64(s.InboundTooLarge.Load()) })
	c.duration = prometheus.NewDesc("multica_ws_compression_write_seconds", "Data write duration, including compression and network backpressure.", []string{"endpoint"}, nil)
	c.reloadErrors = prometheus.NewDesc("multica_ws_compression_reload_errors_total", "Rejected compression policy reload attempts; last good policy retained.", nil, nil)
	return c
}
func (c *WSCompressionCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.metrics {
		ch <- m.desc
	}
	ch <- c.duration
	ch <- c.reloadErrors
}
func (c *WSCompressionCollector) Collect(ch chan<- prometheus.Metric) {
	for _, endpoint := range []wscompress.Endpoint{wscompress.Realtime, wscompress.Daemon} {
		s := wscompress.Stats(endpoint)
		for _, m := range c.metrics {
			ch <- prometheus.MustNewConstMetric(m.desc, m.kind, m.value(s), string(endpoint))
		}
		// Independent atomic reads are not a snapshot. Clamp descending buckets
		// so concurrent observations cannot produce an invalid histogram.
		count := s.WriteAttempts.Load()
		buckets := make(map[float64]uint64, len(wscompress.WriteBounds))
		upper := count
		for i := len(wscompress.WriteBounds) - 1; i >= 0; i-- {
			upper = min(upper, s.WriteBuckets[i].Load())
			buckets[wscompress.WriteBounds[i]] = upper
		}
		ch <- prometheus.MustNewConstHistogram(c.duration, count, float64(s.WriteNanos.Load())/1e9, buckets, string(endpoint))
	}
	ch <- prometheus.MustNewConstMetric(c.reloadErrors, prometheus.CounterValue, float64(wscompress.ReloadErrors.Load()))
}
