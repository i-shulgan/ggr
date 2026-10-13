package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aerokube/ggr/config"
	"github.com/prometheus/client_golang/prometheus/testutil"
	assert "github.com/stretchr/testify/require"
)

func enableStatus(t *testing.T) {
	setGlobal(t, &statusInterval, time.Minute)
	resetHostStatuses()
	t.Cleanup(resetHostStatuses)
}

func TestPrefersHostWithFreeSlots(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableStatus(t)
	full, free := newFakeSelenoid(t, 2), newFakeSelenoid(t, 3)
	fill(t, full)
	updateQuota(user, quotaOf(full.host, free.host))
	pollStatuses(context.Background())
	assert.Equal(t, 3.0, testutil.ToFloat64(metricFreeSlots.WithLabelValues(free.host.Net())))
	assert.Equal(t, 0.0, testutil.ToFloat64(metricFreeSlots.WithLabelValues(full.host.Net())))

	for i := 0; i < 3; i++ {
		code, msg := createSessionStatus(t)
		assert.Equal(t, http.StatusOK, code, msg)
	}
	n, _, _ := full.counters()
	assert.Equal(t, 0, n, "a host known to be full is not tried while another one has free slots")
	assert.Len(t, free.usedIDs(), 3)

	free.mu.Lock()
	free.total = 10
	free.mu.Unlock()
	// Status is stale now: attempts since the last poll used up the free
	// slots, so both hosts are full and get tried in turn.
	code, _ := createSessionStatus(t)
	assert.Equal(t, http.StatusOK, code)

	pollStatuses(context.Background())
	freeSlotsOfFree, ok := freeSlots(free.host.Net())
	assert.True(t, ok)
	assert.Equal(t, 6, freeSlotsOfFree, "a poll resets attempts counted since the previous one")
}

func TestPickWeightsByFreeSlots(t *testing.T) {
	enableStatus(t)
	hosts := config.Hosts{{Name: "a", Port: 1, Count: 1}, {Name: "b", Port: 1, Count: 1}, {Name: "c", Port: 1, Count: 100}}
	hostStatuses.Lock()
	hostStatuses.m["a:1"] = &hostStatus{total: 10, used: 9, updated: time.Now()}
	hostStatuses.m["b:1"] = &hostStatus{total: 10, used: 0, pending: 1, updated: time.Now()}
	hostStatuses.m["c:1"] = &hostStatus{total: 10, used: 10, updated: time.Now()}
	hostStatuses.Unlock()

	picked := map[string]int{}
	for i := 0; i < 2000; i++ {
		h, idx := pick(hosts)
		assert.Equal(t, hosts[idx].Name, h.Name)
		picked[h.Name]++
	}
	assert.Zero(t, picked["c"], "a full host must not win over hosts with free slots, whatever its count")
	assert.Greater(t, picked["b"], 4*picked["a"], "9 free slots against 1: %v", picked)
	assert.Positive(t, picked["a"])
}

func TestPickFallsBackToCountWithoutStatus(t *testing.T) {
	enableStatus(t)
	hosts := config.Hosts{{Name: "a", Port: 1, Count: 1}, {Name: "b", Port: 1, Count: 0}, {Name: "stale", Port: 1, Count: 1}}
	hostStatuses.Lock()
	hostStatuses.m["stale:1"] = &hostStatus{total: 10, updated: time.Now().Add(-time.Hour)}
	hostStatuses.Unlock()
	picked := map[string]int{}
	for i := 0; i < 200; i++ {
		h, _ := pick(hosts)
		picked[h.Name]++
	}
	assert.Zero(t, picked["b"], "count 0 disables a host")
	assert.Positive(t, picked["a"])
	assert.Positive(t, picked["stale"], "a stale status is ignored")

	full := config.Hosts{{Name: "full", Port: 1, Count: 1}}
	hostStatuses.Lock()
	hostStatuses.m["full:1"] = &hostStatus{total: 1, used: 1, updated: time.Now()}
	hostStatuses.Unlock()
	h, _ := pick(full)
	assert.Equal(t, "full", h.Name, "a full host is still tried when nothing else is left")
}

func TestStatusUnavailableKeepsWeightedRandom(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableStatus(t)
	grid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"value":{"ready":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":{"sessionId":"1","capabilities":{}}}`))
	}))
	defer grid.Close()
	host, port := hostportnum(grid.URL)
	node := singleHostNode(host, port)
	down := deadHost(t)
	updateQuota(user, quotaOf(node, down))

	pollStatuses(context.Background())
	_, ok := freeSlots(node.Net())
	assert.False(t, ok, "a /status without Selenoid counters is unknown")
	_, ok = freeSlots(down.Net())
	assert.False(t, ok)
	code, _ := createSessionStatus(t)
	assert.Equal(t, http.StatusOK, code)
}

func TestStatusPollerSendsHostCredentials(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	enableStatus(t)
	auth := make(chan string, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		auth <- u + ":" + p
		_, _ = w.Write([]byte(`{"total":5,"used":1,"pending":1}`))
	}))
	defer s.Close()
	host, port := hostportnum(s.URL)
	node := config.Host{Name: host, Port: port, Count: 1, Username: "u", Password: "p"}
	updateQuota(user, quotaOf(node))

	pollStatuses(context.Background())
	assert.Equal(t, "u:p", <-auth)
	n, ok := freeSlots(node.Net())
	assert.True(t, ok)
	assert.Equal(t, 3, n)
}

func TestStatusPollerRuns(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	setGlobal(t, &statusInterval, 20*time.Millisecond)
	resetHostStatuses()
	t.Cleanup(resetHostStatuses)
	f := newFakeSelenoid(t, 4)
	updateQuota(user, quotaOf(f.host))

	ctx, cancel := context.WithCancel(context.Background())
	done := startStatusPoller(ctx)
	defer func() {
		cancel()
		<-done
	}()
	waitFor(t, "a status poll", func() bool { _, ok := freeSlots(f.host.Net()); return ok })
	fill(t, f)
	waitFor(t, "the next poll", func() bool { n, _ := freeSlots(f.host.Net()); return n == 0 })
}
