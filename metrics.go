package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Failure reasons of a single attempt to create a session on a host.
const (
	reasonBusy        = "busy"
	reasonStartFailed = "start_failed"
	reasonHostError   = "host_error"
)

// Results of a whole new session request.
const (
	resultCreated     = "created"
	resultFailed      = "failed"
	resultTimeout     = "queue_timeout"
	resultQueueFull   = "queue_full"
	resultCanceled    = "canceled"
	resultUnsupported = "unsupported"
	resultBadResponse = "bad_response"
)

var (
	metricAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ggr_session_attempts_total",
		Help: "Attempts to create a session, by host.",
	}, []string{"host"})

	metricFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ggr_session_failures_total",
		Help: "Failed attempts to create a session, by host and reason (busy, start_failed, host_error).",
	}, []string{"host", "reason"})

	metricSessionsCreated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ggr_sessions_created_total",
		Help: "Sessions created, by host.",
	}, []string{"host"})

	metricRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ggr_session_requests_total",
		Help: "New session requests, by result.",
	}, []string{"result"})

	metricBreakerOpen = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ggr_host_breaker_open",
		Help: "1 while the circuit breaker of a host is open.",
	}, []string{"host"})

	metricQueueLength = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ggr_queue_length",
		Help: "New session requests waiting for a free host.",
	})

	metricQueueWait = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ggr_queue_wait_seconds",
		Help:    "Time new session requests spent in the queue, by result.",
		Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"result"})

	metricFreeSlots = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ggr_host_free_slots",
		Help: "Free slots of a host from its last /status (total - used - pending).",
	}, []string{"host"})

	metricOrphansDeleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ggr_orphan_sessions_deleted_total",
		Help: "Sessions deleted because the client went away before receiving them, by result (ok, error).",
	}, []string{"result"})
)
