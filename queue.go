package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

var (
	// queueTimeout is how long a new session request may wait for a free
	// host. Zero disables queueing: like upstream, the request fails as
	// soon as every host was tried once.
	queueTimeout time.Duration
	// maxQueue limits waiting requests; more get 503. Zero means no limit.
	maxQueue        int
	queueBackoff    = 250 * time.Millisecond
	queueBackoffMax = 5 * time.Second

	queueLength atomic.Int64
)

// queueEnter takes a place in the queue unless it is full.
func queueEnter() (int64, bool) {
	for {
		n := queueLength.Load()
		if maxQueue > 0 && n >= int64(maxQueue) {
			return n, false
		}
		if queueLength.CompareAndSwap(n, n+1) {
			metricQueueLength.Inc()
			return n + 1, true
		}
	}
}

func queueLeave() {
	queueLength.Add(-1)
	metricQueueLength.Dec()
}

// backoff returns the delay before pass n+1 over the hosts: exponential
// from queueBackoff up to queueBackoffMax, with equal jitter so that waiting
// requests do not hit hosts at the same moments.
func backoff(n int) time.Duration {
	d := queueBackoffMax
	if n < 32 {
		if e := queueBackoff << n; e > 0 && e < d {
			d = e
		}
	}
	if d <= 0 {
		return 0
	}
	half := d / 2
	return half + rand.N(d-half+1)
}

func retryAfter() string {
	secs := int(math.Ceil(queueBackoffMax.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

// wait repeats passes over the hosts until a session is created, the queue
// timeout expires or the client goes away.
func (s *newSession) wait(w http.ResponseWriter, r *http.Request) {
	n, ok := queueEnter()
	if !ok {
		w.Header().Set("Retry-After", retryAfter())
		s.fail(w, http.StatusServiceUnavailable, fmt.Sprintf("queue is full (%d waiting)", n), resultQueueFull)
		return
	}
	queued := time.Now()
	defer func() {
		queueLeave()
		metricQueueWait.WithLabelValues(s.result).Observe(time.Since(queued).Seconds())
	}()
	s.log("SESSION_QUEUED", "", "", false, fmt.Sprintf("queue length %d", n))
	deadline := queued.Add(queueTimeout)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for i := 0; ; i++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			s.fail(w, http.StatusInternalServerError, fmt.Sprintf("queue timeout %s exceeded", queueTimeout), resultTimeout)
			return
		}
		timer.Reset(min(backoff(i), remaining))
		select {
		case <-r.Context().Done():
			s.finish(resultCanceled)
			s.log("CLIENT_DISCONNECTED", "", "", false, "while queued")
			return
		case <-timer.C:
		}
		switch s.round(w, r) {
		case roundDone:
			return
		case roundExhausted:
			s.fail(w, http.StatusInternalServerError, "", resultFailed)
			return
		}
	}
}
