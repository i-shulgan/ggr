package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aerokube/ggr/config"
	assert "github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func singleHostQuota(h config.Host) config.Browsers {
	return config.Browsers{Browsers: []config.Browser{
		{Name: "browser", DefaultVersion: "1.0", Versions: []config.Version{
			{Number: "1.0", Regions: []config.Region{
				{Name: "1", Hosts: config.Hosts{h}},
			}},
		}}}}
}

func TestProxySendsHostCredentials(t *testing.T) {
	var got []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		got = append(got, r.URL.Path+" "+u+":"+p)
		_, _ = w.Write([]byte(`{"value":{}}`))
	})
	selenium := httptest.NewServer(mux)
	defer selenium.Close()

	host, port := hostportnum(selenium.URL)
	node := config.Host{Name: host, Port: port, Count: 1, Username: "host-user", Password: "host-password"}

	test.Lock()
	defer test.Unlock()
	updateQuota(user, singleHostQuota(node))

	sessionID := node.Sum() + "123"
	for _, p := range []string{
		"/wd/hub/session/" + sessionID + "/url",
		"/logs/" + sessionID,
		"/video/" + sessionID,
		"/download/" + sessionID + "/file.txt",
		"/clipboard/" + sessionID,
	} {
		rsp, err := doBasicHTTPRequest(http.MethodGet, gridrouter(p), nil)
		assert.NoError(t, err)
		_ = rsp.Body.Close()
		assert.Equal(t, http.StatusOK, rsp.StatusCode, p)
	}
	rsp, err := http.Get(gridrouter("/devtools/" + sessionID + "/page"))
	assert.NoError(t, err)
	_ = rsp.Body.Close()

	assert.Equal(t, []string{
		"/wd/hub/session/123/url host-user:host-password",
		"/logs/123.log host-user:host-password",
		"/video/123.mp4 host-user:host-password",
		"/download/123/file.txt host-user:host-password",
		"/clipboard/123 host-user:host-password",
		"/devtools/123/page host-user:host-password",
	}, got)
}

func TestProxyKeepsClientCredentialsWithoutHostCredentials(t *testing.T) {
	var got string
	selenium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		got = u + ":" + p
		_, _ = w.Write([]byte(`{"value":{}}`))
	}))
	defer selenium.Close()

	host, port := hostportnum(selenium.URL)
	node := config.Host{Name: host, Port: port, Count: 1}

	test.Lock()
	defer test.Unlock()
	updateQuota(user, singleHostQuota(node))

	rsp, err := doBasicHTTPRequest(http.MethodGet, gridrouter("/wd/hub/session/"+node.Sum()+"123/url"), nil)
	assert.NoError(t, err)
	_ = rsp.Body.Close()
	assert.Equal(t, user+":"+password, got)
}

func TestProxyWebSocketVNCSendsHostCredentials(t *testing.T) {
	const testData = "ws-data"
	auth := make(chan string, 1)
	mux := http.NewServeMux()
	mux.Handle("/vnc/123", websocket.Handler(func(wsconn *websocket.Conn) {
		u, p, _ := wsconn.Request().BasicAuth()
		auth <- u + ":" + p
		_, _ = wsconn.Write([]byte(testData))
	}))
	wsServer := httptest.NewServer(mux)
	defer wsServer.Close()

	wsHost := config.Host{Name: "example.com", Port: 4444, Count: 1, Username: "host-user", Password: "host-password",
		VNC: fmt.Sprintf("ws://%s/vnc", wsServer.Listener.Addr().String())}

	test.Lock()
	defer test.Unlock()
	updateQuota(user, singleHostQuota(wsHost))

	testDataReceived(wsHost, "vnc", testData, t)
	assert.Equal(t, "host-user:host-password", <-auth)
}

func TestCreateVNCInfoDefaultPorts(t *testing.T) {
	for vnc, want := range map[string]config.VncInfo{
		"ws://example.com/vnc":       {Scheme: "ws", Host: "example.com", Port: "80", Path: "/vnc"},
		"wss://example.com/vnc":      {Scheme: "wss", Host: "example.com", Port: "443", Path: "/vnc"},
		"wss://example.com:8443/vnc": {Scheme: "wss", Host: "example.com", Port: "8443", Path: "/vnc"},
		"vnc://example.com":          {Scheme: "vnc", Host: "example.com", Port: "5900"},
	} {
		got := createVNCInfo(config.Host{Name: "h", Port: 1, VNC: vnc})
		assert.NotNil(t, got, vnc)
		assert.Equal(t, want, *got, vnc)
	}
}

func TestStartSessionRewritesCDPEndpoint(t *testing.T) {
	selenium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":{"sessionId":"123","capabilities":{"browserName":"chrome","se:cdp":"ws://` + r.Host + `/devtools/123/"}}}`))
	}))
	defer selenium.Close()

	host, port := hostportnum(selenium.URL)
	node := config.Host{Name: host, Port: port, Count: 1}

	test.Lock()
	defer test.Unlock()
	updateQuota(user, singleHostQuota(node))

	rsp, err := createSession(`{"capabilities":{"alwaysMatch":{"browserName":"browser"}}}`)
	assert.NoError(t, err)
	defer rsp.Body.Close()
	var reply struct {
		Value struct {
			SessionID    string                 `json:"sessionId"`
			Capabilities map[string]interface{} `json:"capabilities"`
		} `json:"value"`
	}
	assert.NoError(t, json.NewDecoder(rsp.Body).Decode(&reply))
	assert.Equal(t, node.Sum()+"123", reply.Value.SessionID)
	assert.Equal(t, "ws://"+hostport(srv.URL)+"/devtools/"+node.Sum()+"123/", reply.Value.Capabilities["se:cdp"])
}
