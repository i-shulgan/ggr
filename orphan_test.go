package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	assert "github.com/stretchr/testify/require"
)

func TestClientGoneSessionDeleted(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	f := newFakeSelenoid(t, 1)
	updateQuota(user, quotaOf(f.host))

	ctx, cancel := context.WithCancel(context.Background())
	f.onCreate = func(r *http.Request) {
		// The client gives up while the browser is starting.
		cancel()
		time.Sleep(200 * time.Millisecond)
	}
	before := testutil.ToFloat64(metricOrphansDeleted.WithLabelValues("ok"))
	canceled := testutil.ToFloat64(metricRequests.WithLabelValues(resultCanceled))

	r := <-createSessionAsync(ctx)
	assert.Error(t, r.err)
	// The fake host records the DELETE before Ggr reads its answer, so wait
	// for the metric that Ggr updates last.
	waitFor(t, "the orphan session to be deleted", func() bool {
		return testutil.ToFloat64(metricOrphansDeleted.WithLabelValues("ok")) == before+1
	})
	_, _, deleted := f.counters()
	assert.Equal(t, []string{"s1"}, deleted)
	assert.Empty(t, f.usedIDs(), "the slot must be freed")
	assert.Equal(t, before+1, testutil.ToFloat64(metricOrphansDeleted.WithLabelValues("ok")))
	assert.Equal(t, canceled+1, testutil.ToFloat64(metricRequests.WithLabelValues(resultCanceled)))
}

func TestOrphanGraceExpires(t *testing.T) {
	test.Lock()
	defer test.Unlock()
	setGlobal(t, &orphanGrace, 50*time.Millisecond)
	f := newFakeSelenoid(t, 1)
	updateQuota(user, quotaOf(f.host))

	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	f.onCreate = func(r *http.Request) {
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		close(release)
	}
	start := time.Now()
	r := <-createSessionAsync(ctx)
	assert.Error(t, r.err)
	<-release
	assert.Less(t, time.Since(start), 2*time.Second, "the attempt is aborted after the grace period")
}

func TestAttemptContext(t *testing.T) {
	setGlobal(t, &orphanGrace, 20*time.Millisecond)
	client, cancelClient := context.WithCancel(context.Background())
	ctx, cancel := attemptContext(client)
	defer cancel()
	cancelClient()
	assert.NoError(t, ctx.Err(), "the attempt outlives the client for the grace period")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the attempt must be cancelled after the grace period")
	}

	ctx, cancel = attemptContext(context.Background())
	cancel()
	assert.Error(t, ctx.Err())
}
