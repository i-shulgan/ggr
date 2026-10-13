package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	assert "github.com/stretchr/testify/require"
)

func createSessionStatus(t *testing.T) (int, string) {
	rsp, err := createSession(w3cCaps)
	assert.NoError(t, err)
	return rsp.StatusCode, message(rsp)
}

func TestBreakerSkipsDeadHostAcrossRequests(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	setGlobal(t, &breakerTimeout, 300*time.Millisecond)
	resetBreakers()
	t.Cleanup(resetBreakers)

	dead := deadHost(t)
	alive := newFakeSelenoid(t, 100)
	updateQuota(user, quotaOf(dead, alive.host))
	deadAttempts := func() float64 { return testutil.ToFloat64(metricAttempts.WithLabelValues(dead.Net())) }

	before := deadAttempts()
	for i := 0; i < 50 && deadAttempts() == before; i++ {
		code, _ := createSessionStatus(t)
		assert.Equal(t, http.StatusOK, code, "the alive host must take over")
	}
	assert.Equal(t, before+1, deadAttempts(), "the dead host must be tried")
	assert.Equal(t, 1.0, testutil.ToFloat64(metricBreakerOpen.WithLabelValues(dead.Net())))

	for i := 0; i < 20; i++ {
		code, _ := createSessionStatus(t)
		assert.Equal(t, http.StatusOK, code)
	}
	assert.Equal(t, before+1, deadAttempts(), "an open breaker must keep other requests away from the host")

	time.Sleep(350 * time.Millisecond)
	for i := 0; i < 50 && deadAttempts() == before+1; i++ {
		code, _ := createSessionStatus(t)
		assert.Equal(t, http.StatusOK, code)
	}
	assert.Equal(t, before+2, deadAttempts(), "after the timeout the host gets a probe")
	assert.Equal(t, 1.0, testutil.ToFloat64(metricBreakerOpen.WithLabelValues(dead.Net())), "a failed probe opens the breaker again")
}

func TestBreakerOnlyHostDown(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	setGlobal(t, &breakerTimeout, 200*time.Millisecond)
	resetBreakers()
	t.Cleanup(resetBreakers)

	dead := deadHost(t)
	updateQuota(user, quotaOf(dead))

	code, msg := createSessionStatus(t)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, msg, "after 1 attempt(s)")

	code, msg = createSessionStatus(t)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, msg, "after 0 attempt(s)")
	assert.Contains(t, msg, "temporarily unavailable")

	time.Sleep(250 * time.Millisecond)
	code, msg = createSessionStatus(t)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, msg, "after 1 attempt(s)", "the host is probed again after the timeout")
}

func TestBreakerClosesWhenHostRecovers(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	setGlobal(t, &breakerTimeout, time.Minute)
	resetBreakers()
	t.Cleanup(resetBreakers)

	f := newFakeSelenoid(t, 10)
	breakerFailure(f.host.Net())
	assert.False(t, breakerAvailable(f.host.Net()))
	breakers.Lock()
	breakers.m[f.host.Net()].openUntil = time.Now()
	breakers.Unlock()
	assert.True(t, breakerAvailable(f.host.Net()))

	updateQuota(user, quotaOf(f.host))
	code, _ := createSessionStatus(t)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 0.0, testutil.ToFloat64(metricBreakerOpen.WithLabelValues(f.host.Net())))
	breakers.Lock()
	_, tracked := breakers.m[f.host.Net()]
	breakers.Unlock()
	assert.False(t, tracked)
}

func TestBreakerAllowsOneProbe(t *testing.T) {
	setGlobal(t, &breakerTimeout, time.Millisecond)
	resetBreakers()
	t.Cleanup(resetBreakers)

	breakerFailure("h:1")
	time.Sleep(2 * time.Millisecond)
	assert.True(t, breakerAcquire("h:1"))
	assert.False(t, breakerAcquire("h:1"), "only one probe at a time")
	assert.False(t, breakerAvailable("h:1"))
	breakerRelease("h:1")
	assert.True(t, breakerAcquire("h:1"))
}

func TestBreakerThreshold(t *testing.T) {
	setGlobal(t, &breakerTimeout, time.Minute)
	setGlobal(t, &breakerThreshold, 3)
	resetBreakers()
	t.Cleanup(resetBreakers)

	breakerFailure("h:1")
	breakerFailure("h:1")
	assert.True(t, breakerAvailable("h:1"))
	breakerSuccess("h:1")
	breakerFailure("h:1")
	breakerFailure("h:1")
	assert.True(t, breakerAvailable("h:1"), "a success resets consecutive failures")
	breakerFailure("h:1")
	assert.False(t, breakerAvailable("h:1"))
}

func TestStartFailuresAreLimited(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	a, b, c := newFakeSelenoid(t, 1), newFakeSelenoid(t, 1), newFakeSelenoid(t, 1)
	for _, f := range []*fakeSelenoid{a, b, c} {
		f.setFailStart(true)
	}
	updateQuota(user, quotaOf(a.host, b.host, c.host))

	code, msg := createSessionStatus(t)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, msg, "after 3 attempt(s)", "by default every host is tried like upstream")
	assert.Contains(t, msg, "failed to start browser")

	setGlobal(t, &maxStartFailures, 2)
	code, msg = createSessionStatus(t)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, msg, "after 2 attempt(s)")
	assert.Contains(t, msg, "gave up after 2 browser start failure(s)")

	total := 0
	for _, f := range []*fakeSelenoid{a, b, c} {
		n, _, _ := f.counters()
		assert.LessOrEqual(t, n, 2, "a host that failed to start a browser is not tried twice per request")
		total += n
	}
	assert.Equal(t, 5, total)
}

func TestBusyIsNotStartFailure(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	setGlobal(t, &maxStartFailures, 1)
	busy, free := newFakeSelenoid(t, 0), newFakeSelenoid(t, 1)
	updateQuota(user, quotaOf(busy.host))

	busyBefore := testutil.ToFloat64(metricFailures.WithLabelValues(busy.host.Net(), reasonBusy))
	code, msg := createSessionStatus(t)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, msg, "Too Many Requests")
	assert.Equal(t, busyBefore+1, testutil.ToFloat64(metricFailures.WithLabelValues(busy.host.Net(), reasonBusy)))

	// A busy host does not use up -max-start-failures.
	updateQuota(user, quotaOf(busy.host, free.host))
	for i := 0; i < 10; i++ {
		code, _ = createSessionStatus(t)
		assert.Equal(t, http.StatusOK, code)
		for _, id := range free.usedIDs() {
			free.release(id)
		}
	}
}
