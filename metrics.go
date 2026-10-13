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

)
