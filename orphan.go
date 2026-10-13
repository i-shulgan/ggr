package main

import (
	"context"
	"net/http"
	"time"

	"github.com/aerokube/ggr/config"
)

// orphanGrace is how long an attempt to create a session goes on after the
// client went away. Selenoid keeps a session it created even if Ggr stopped
// waiting for the answer, so aborting at once could leave it running until
// its idle timeout; waiting a bit lets Ggr learn the session ID and delete it.
var orphanGrace = 30 * time.Second

// attemptContext is cancelled orphanGrace after the client went away, or
// when the returned cancel function is called.
func attemptContext(client context.Context) (context.Context, context.CancelFunc) {
	grace := orphanGrace
	ctx, cancel := context.WithCancel(context.WithoutCancel(client))
	stop := context.AfterFunc(client, func() {
		select {
		case <-time.After(grace):
			cancel()
		case <-ctx.Done():
		}
	})
	return ctx, func() {
		stop()
		cancel()
	}
}

// sessionIDOf returns the session ID from a new session response in either
// JSON Wire or W3C format.
func sessionIDOf(resp map[string]interface{}) (string, bool) {
	if sess, ok := resp["sessionId"].(string); ok {
		return sess, true
	}
	value, _ := resp["value"].(map[string]interface{})
	sess, ok := value["sessionId"].(string)
	return sess, ok
}

// deleteOrphan deletes a session created for a client that is gone.
func (s *newSession) deleteOrphan(h *config.Host, sess string) {
	ctx, cancel := context.WithTimeout(context.Background(), min(timeout, time.Minute))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, sessionURL(h)+"/"+sess, nil)
	if err == nil {
		setHostAuth(req.Header, h)
		var resp *http.Response
		resp, err = httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= http.StatusInternalServerError {
				err = &statusError{resp.Status}
			}
		}
	}
	if err != nil {
		metricOrphansDeleted.WithLabelValues("error").Inc()
		s.log("ORPHAN_SESSION_NOT_DELETED", h.Net(), sess, true, err.Error())
		return
	}
	metricOrphansDeleted.WithLabelValues("ok").Inc()
	s.log("ORPHAN_SESSION_DELETED", h.Net(), sess, true, "")
}
