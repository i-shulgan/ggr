package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	assert "github.com/stretchr/testify/require"
)

func TestMetricsEndpoint(t *testing.T) {
	selenium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":{"sessionId":"123","capabilities":{}}}`))
	}))
	defer selenium.Close()
	host, port := hostportnum(selenium.URL)
	node := singleHostNode(host, port)

	test.Lock()
	defer test.Unlock()
	updateQuota(user, singleHostQuota(node))

	created := testutil.ToFloat64(metricSessionsCreated.WithLabelValues(node.Net()))
	attempts := testutil.ToFloat64(metricAttempts.WithLabelValues(node.Net()))
	rsp, err := createSession(`{"capabilities":{"alwaysMatch":{"browserName":"browser"}}}`)
	assert.NoError(t, err)
	_ = rsp.Body.Close()
	assert.Equal(t, http.StatusOK, rsp.StatusCode)
	assert.Equal(t, created+1, testutil.ToFloat64(metricSessionsCreated.WithLabelValues(node.Net())))
	assert.Equal(t, attempts+1, testutil.ToFloat64(metricAttempts.WithLabelValues(node.Net())))

	rsp, err = http.Get(gridrouter("/metrics"))
	assert.NoError(t, err)
	defer rsp.Body.Close()
	body, _ := io.ReadAll(rsp.Body)
	assert.Equal(t, http.StatusOK, rsp.StatusCode)
	for _, name := range []string{"ggr_session_attempts_total", "ggr_sessions_created_total", "ggr_session_requests_total"} {
		assert.Contains(t, string(body), name)
	}
}
