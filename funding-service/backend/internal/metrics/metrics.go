package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	TicksReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "funding_ticks_received_total",
		Help: "Total number of ticks ingested, by source and symbol.",
	}, []string{"source", "symbol"})

	WSClients = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "funding_ws_clients",
		Help: "Current number of connected WebSocket clients.",
	})

	SnapshotLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "funding_snapshot_latency_seconds",
		Help:    "Time from snapshot creation to WebSocket broadcast.",
		Buckets: prometheus.DefBuckets,
	})

	CBPublications = promauto.NewCounter(prometheus.CounterOpts{
		Name: "funding_cb_publications_detected_total",
		Help: "Total number of new CBR rate publications detected.",
	})

	CBRFetchDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "funding_cbr_fetch_duration_seconds",
		Help:    "Latency of a single CBR HTTP poll request (the time to fetch and parse the rate XML).",
		Buckets: prometheus.DefBuckets,
	})
)
