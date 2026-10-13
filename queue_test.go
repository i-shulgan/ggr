package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	assert "github.com/stretchr/testify/require"
)

func enableQueue(t *testing.T, timeout time.Duration, limit int) {
	setGlobal(t, &queueTimeout, timeout)
	setGlobal(t, &maxQueue, limit)
	setGlobal(t, &queueBackoff, 10*time.Millisecond)
	setGlobal(t, &queueBackoffMax, 50*time.Millisecond)
}

type sessionResult struct {
	code int
	msg  string
	err  error
	took time.Duration
}

func createSessionAsync(ctx context.Context) chan sessionResult {
	ret := make(chan sessionResult, 1)
	go func() {
		start := time.Now()
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, gridrouter("/wd/hub/session"), strings.NewReader(w3cCaps))
		r.SetBasicAuth(user, password)
		rsp, err := http.DefaultClient.Do(r)
		if err != nil {
			ret <- sessionResult{err: err, took: time.Since(start)}
			return
		}
		ret <- sessionResult{code: rsp.StatusCode, msg: message(rsp), took: time.Since(start)}
	}()
	return ret
}

func waitFor(t *testing.T, what string, cond func() bool) {
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fill occupies all slots of the hosts directly.
func fill(t *testing.T, hosts ...*fakeSelenoid) {
	for _, f := range hosts {
		f.mu.Lock()
		for len(f.used) < f.total {
			f.nextID++
			f.used["busy"+string(rune('a'+f.nextID))] = true
		}
		f.mu.Unlock()
	}
}

func TestQueueWaitsUntilSlotIsFreed(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableQueue(t, 5*time.Second, 0)
	a, b := newFakeSelenoid(t, 1), newFakeSelenoid(t, 1)
	fill(t, a, b)
	updateQuota(user, quotaOf(a.host, b.host))

	res := createSessionAsync(context.Background())
	waitFor(t, "the request to be queued", func() bool { return queueLength.Load() == 1 })
	aAttempts, _, _ := a.counters()
	bAttempts, _, _ := b.counters()
	waitFor(t, "a second pass over the hosts", func() bool {
		na, _, _ := a.counters()
		nb, _, _ := b.counters()
		return na > aAttempts && nb > bAttempts
	})
	select {
	case r := <-res:
		t.Fatalf("request finished while all hosts are busy: %+v", r)
	default:
	}

	a.release(a.usedIDs()[0])
	r := <-res
	assert.NoError(t, r.err)
	assert.Equal(t, http.StatusOK, r.code, r.msg)
	assert.Len(t, a.usedIDs(), 1, "the session goes to the host that freed a slot")
	assert.EqualValues(t, 0, queueLength.Load())
	assert.Equal(t, 0.0, testutil.ToFloat64(metricQueueLength))

	attempts, noWait, _ := a.counters()
	assert.Equal(t, attempts, noWait, "every attempt must ask Selenoid not to queue")
}

func TestQueueRetriesSingleHost(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableQueue(t, 5*time.Second, 0)
	a := newFakeSelenoid(t, 1)
	fill(t, a)
	updateQuota(user, quotaOf(a.host))

	res := createSessionAsync(context.Background())
	waitFor(t, "several attempts on the busy host", func() bool { n, _, _ := a.counters(); return n >= 3 })
	a.release(a.usedIDs()[0])
	r := <-res
	assert.Equal(t, http.StatusOK, r.code, r.msg)
}

func TestQueueTimeout(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableQueue(t, 300*time.Millisecond, 0)
	a := newFakeSelenoid(t, 1)
	fill(t, a)
	updateQuota(user, quotaOf(a.host))

	before := testutil.ToFloat64(metricRequests.WithLabelValues(resultTimeout))
	r := <-createSessionAsync(context.Background())
	assert.Equal(t, http.StatusInternalServerError, r.code)
	assert.Contains(t, r.msg, "queue timeout 300ms exceeded")
	assert.Contains(t, r.msg, "Too Many Requests")
	assert.GreaterOrEqual(t, r.took, 300*time.Millisecond)
	assert.Less(t, r.took, 2*time.Second)
	assert.Equal(t, before+1, testutil.ToFloat64(metricRequests.WithLabelValues(resultTimeout)))
}

func TestQueueOverflowReturns503(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableQueue(t, 5*time.Second, 1)
	a := newFakeSelenoid(t, 1)
	fill(t, a)
	updateQuota(user, quotaOf(a.host))

	first := createSessionAsync(context.Background())
	waitFor(t, "the first request to be queued", func() bool { return queueLength.Load() == 1 })

	rsp, err := createSession(w3cCaps)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rsp.StatusCode)
	assert.Equal(t, "1", rsp.Header.Get("Retry-After"))
	assert.Contains(t, message(rsp), "queue is full")

	a.release(a.usedIDs()[0])
	r := <-first
	assert.Equal(t, http.StatusOK, r.code, r.msg)
}

func TestQueueClientGoesAway(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableQueue(t, 5*time.Second, 0)
	a := newFakeSelenoid(t, 1)
	fill(t, a)
	updateQuota(user, quotaOf(a.host))

	ctx, cancel := context.WithCancel(context.Background())
	res := createSessionAsync(ctx)
	waitFor(t, "the request to be queued", func() bool { return queueLength.Load() == 1 })
	cancel()
	assert.Error(t, (<-res).err)
	waitFor(t, "the request to leave the queue", func() bool { return queueLength.Load() == 0 })
	n, _, _ := a.counters()
	time.Sleep(100 * time.Millisecond)
	m, _, _ := a.counters()
	assert.Equal(t, n, m, "no attempts after the client went away")
}

func TestNoQueueByDefault(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	a, b := newFakeSelenoid(t, 1), newFakeSelenoid(t, 1)
	fill(t, a, b)
	updateQuota(user, quotaOf(a.host, b.host))

	r := <-createSessionAsync(context.Background())
	assert.Equal(t, http.StatusInternalServerError, r.code)
	assert.Contains(t, r.msg, "after 2 attempt(s)")
	_, noWaitA, _ := a.counters()
	_, noWaitB, _ := b.counters()
	assert.Equal(t, 1, noWaitA+noWaitB, "like upstream, the last host is asked without X-Selenoid-No-Wait")
}

func TestBackoff(t *testing.T) {
	setGlobal(t, &queueBackoff, 100*time.Millisecond)
	setGlobal(t, &queueBackoffMax, time.Second)
	for i, want := range []time.Duration{100, 200, 400, 800, 1000, 1000} {
		want *= time.Millisecond
		for j := 0; j < 20; j++ {
			d := backoff(i)
			assert.GreaterOrEqual(t, d, want/2)
			assert.LessOrEqual(t, d, want)
		}
	}
	assert.LessOrEqual(t, backoff(100), time.Second)
}
