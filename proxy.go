package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abbot/go-http-auth"
	"github.com/aerokube/ggr/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/websocket"
)

const (
	browserStarted = iota
	browserFailed
	seleniumError
	hostBusy
)

const (
	md5SumLength   = 32
	sessPart       = 4 // /wd/hub/session/{various length session}
	defaultVNCPort = "5900"
	vncScheme      = "vnc"
	wsScheme       = "ws"
	wssScheme      = "wss"
)

var paths = struct {
	Ping, Status, Err, Host, Quota, Route, Proxy, VNC, Video, Logs, Download, Clipboard, Devtools, Pprof, Metrics string
}{
	Ping:      "/ping",
	Status:    "/wd/hub/status",
	Err:       "/err",
	Host:      "/host/",
	Quota:     "/quota",
	Route:     "/wd/hub/session",
	Proxy:     "/wd/hub/session/",
	VNC:       "/vnc/",
	Video:     "/video/",
	Logs:      "/logs/",
	Download:  "/download/",
	Clipboard: "/clipboard/",
	Devtools:  "/devtools/",
	Pprof:     "/debug/pprof/",
	Metrics:   "/metrics",
}

var keys = struct {
	desiredCapabilities string
	w3cCapabilities     string
	alwaysMatch         string
	firstMatch          string
}{
	desiredCapabilities: "desiredCapabilities",
	w3cCapabilities:     "capabilities",
	alwaysMatch:         "alwaysMatch",
	firstMatch:          "firstMatch",
}

var (
	head       = len(paths.Proxy)
	tail       = head + md5SumLength
	httpClient = &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	quota       = make(map[string]ggrBrowsers)
	routes      = make(Routes)
	numSessions uint64
	numRequests uint64
	confLock    sync.RWMutex
)

// Routes - an MD5 to host map
type Routes map[string]*config.Host

type caps map[string]interface{}

func (c caps) capabilities(fn func(m map[string]interface{}, w3c bool, extension bool)) {
	if desiredCapabilities, ok := c[keys.desiredCapabilities]; ok {
		if m, ok := desiredCapabilities.(map[string]interface{}); ok {
			fn(m, false, false)
		}
	}
	if w3cCapabilities, ok := c[keys.w3cCapabilities]; ok {
		if m, ok := w3cCapabilities.(map[string]interface{}); ok {
			var match map[string]interface{}
			if alwaysMatch, ok := m[keys.alwaysMatch]; ok {
				if m, ok := alwaysMatch.(map[string]interface{}); ok {
					match = m
				}
			} else if firstMatch, ok := m[keys.firstMatch]; ok {
				if m, ok := firstMatch.([]interface{}); ok && len(m) > 0 {
					if m, ok := m[0].(map[string]interface{}); ok {
						match = m
					}
				}
			}
			if match != nil {
				fn(match, true, false)
				for k, v := range match { // Extension capabilities have ":" in key
					if ec, ok := v.(map[string]interface{}); ok && strings.Contains(k, ":") {
						fn(ec, true, true)
					}
				}
			}
		}
	} else {
		fn(make(map[string]interface{}), false, false)
	}
}

func (c caps) capability(k string) string {
	return c.capabilityJsonWireW3C(k, k)
}

func (c caps) capabilityJsonWireW3C(jsonWire, W3C string) string {
	result := ""
	c.capabilities(func(m map[string]interface{}, w3c bool, _ bool) {
		k := jsonWire
		if w3c {
			k = W3C
		}
		if v, ok := m[k].(string); ok {
			result = v
		} else if v, ok := m[k].(map[string]interface{}); ok {
			var pairs []string
			for k, v := range v {
				pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
			}
			result = strings.Join(pairs, " ")
		} else if v, ok := m[k]; ok && v != nil {
			log.Printf(`[-] [-] [BAD_CAPABILITY] [Using default value for capability %s: unsupported value type "%s"] [-] [-] [-] [-] [-] [-]`, k, reflect.TypeOf(m[k]).String())
		}
	})
	return result
}

func (c caps) browser() string {
	browserName := c.capability("browserName")
	if browserName != "" {
		return browserName
	}
	appiumDeviceName := c.capability("appium:deviceName")
	if appiumDeviceName != "" {
		return appiumDeviceName
	}
	return c.capability("deviceName")
}

func (c caps) version() string {
	return c.capabilityJsonWireW3C("version", "browserVersion")
}

func (c caps) platform() string {
	platform := c.capability("platform")
	if platform != "" {
		return platform
	}
	return c.capability("platformName")
}

func (c caps) labels() string {
	return c.capability("labels")
}

func (c caps) setVersion(version string) {
	c.capabilities(func(m map[string]interface{}, w3c bool, extension bool) {
		if extension {
			return
		}
		if w3c {
			m["browserVersion"] = version
		} else {
			m["version"] = version
		}
	})
}

func session(ctx context.Context, h *config.Host, header http.Header, c caps) (map[string]interface{}, int) {
	b, _ := json.Marshal(c)
	req, err := http.NewRequest(http.MethodPost, sessionURL(h), bytes.NewReader(b))
	if err != nil {
		return nil, seleniumError
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Del("Accept-Encoding")
	setHostAuth(req.Header, h)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, seleniumError
	}
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	if location != "" {
		l, err := url.Parse(location)
		if err != nil {
			return nil, seleniumError
		}
		fragments := strings.Split(l.Path, "/")
		return map[string]interface{}{"sessionId": fragments[len(fragments)-1], "status": 0, "value": struct{}{}}, browserStarted
	}
	var reply map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&reply)
	if err != nil {
		return nil, seleniumError
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests || isBusyMessage(browserErrMsg(reply)) {
			return reply, hostBusy
		}
		return reply, browserFailed
	}
	return reply, browserStarted
}

// isBusyMessage tells "no free slots" from real browser start errors.
// Selenoid answers "Too Many Requests" when X-Selenoid-No-Wait is set and the
// limit is reached, and "queue is full" when started with -disable-queue.
func isBusyMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "too many requests") || strings.Contains(msg, "queue is full")
}

// setHostAuth replaces the client credentials with the ones from quota when
// the host has them.
func setHostAuth(header http.Header, h *config.Host) {
	if h.Username != "" && h.Password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(h.Username + ":" + h.Password))
		header.Set("Authorization", "Basic "+auth)
	}
}

// rewriteCDP points the Selenium 4 se:cdp capability returned by Selenoid
// (ws://<selenoid>/devtools/<id>/) to the /devtools/ endpoint of Ggr, so that
// clients connect through Ggr with the Ggr session ID.
func rewriteCDP(r *http.Request, value map[string]interface{}, sessionID string) {
	c, ok := value["capabilities"].(map[string]interface{})
	if !ok {
		return
	}
	if _, ok := c["se:cdp"].(string); !ok {
		return
	}
	scheme := "ws"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "wss"
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	c["se:cdp"] = fmt.Sprintf("%s://%s%s%s/", scheme, host, paths.Devtools, sessionID)
}

func reply(w http.ResponseWriter, msg map[string]interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(msg)
}

func serial() uint64 {
	return atomic.AddUint64(&numRequests, 1) - 1
}

func info(r *http.Request) (user, remote string) {
	if guestAccessAllowed {
		user = guestUserName
	} else {
		user = "unknown"
	}
	if u, _, ok := r.BasicAuth(); ok {
		user = u
	}
	remote = r.Header.Get("X-Forwarded-For")
	if remote != "" {
		return
	}
	remote, _, _ = net.SplitHostPort(r.RemoteAddr)
	return
}

func fmtBrowser(browser, version, platform, labels string) string {
	ret := browser
	if version != "" {
		ret += "-" + version
	}
	if platform != "" {
		ret += "-" + platform
	}
	if labels != "" {
		ret += " " + labels
	}
	return ret
}

func browserErrMsg(js map[string]interface{}) string {
	if js == nil {
		return ""
	}
	val, ok := js["value"].(map[string]interface{})
	if !ok {
		return ""
	}
	msg, ok := val["message"].(string)
	if !ok {
		return ""
	}
	return msg
}

func errMsg(msg string) map[string]interface{} {
	return map[string]interface{}{
		"value": map[string]string{
			"message": msg,
		},
		"status": 13,
	}
}

// newSession is the state of one new session request.
type newSession struct {
	id                                 uint64
	start                              time.Time
	user, remote                       string
	browser, version, platform, labels string
	caps                               caps
	browsers                           ggrBrowsers
	// excluded hosts are not tried again during this request: they failed to
	// start a browser, or did not answer while the circuit breaker is off.
	excluded      *setImpl
	attempts      int
	startFailures int
	lastHostError string
	// result is how the request ended, see resultCreated and others.
	result string
}

func (s *newSession) finish(result string) {
	s.result = result
	metricRequests.WithLabelValues(result).Inc()
}

func (s *newSession) fmtBrowser() string {
	return fmtBrowser(s.browser, s.version, s.platform, s.labels)
}

// log writes a line in the usual format:
// [id] [duration] [STATUS] [user] [remote] [browser] [host] [session] [attempt] [message]
func (s *newSession) log(status, host, sess string, attempt bool, msg string) {
	count := "-"
	if attempt {
		count = fmt.Sprint(s.attempts)
	}
	log.Printf("[%d] [%.2fs] [%s] [%s] [%s] [%s] [%s] [%s] [%s] [%s]\n", s.id, secondsSince(s.start), status, s.user, s.remote, s.fmtBrowser(), dash(host), dash(sess), count, dash(msg))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// roundResult says what happened during one pass over the hosts.
type roundResult int

const (
	// roundDone means that the response was sent or the client went away.
	roundDone roundResult = iota
	// roundBusy means that some hosts may accept the session later: they
	// were busy or closed by the circuit breaker.
	roundBusy
	// roundExhausted means that no host is left to try for this request.
	roundExhausted
)

func route(w http.ResponseWriter, r *http.Request) {
	s := &newSession{start: time.Now(), id: serial()}
	s.user, s.remote = info(r)
	var c caps
	err := json.NewDecoder(r.Body).Decode(&c)
	if err != nil {
		reply(w, errMsg(fmt.Sprintf("bad json format: %s", err.Error())), http.StatusBadRequest)
		log.Printf("[%d] [%.2fs] [BAD_JSON] [%s] [%s] [-] [-] [-] [-] [%v]\n", s.id, secondsSince(s.start), s.user, s.remote, err)
		return
	}
	s.caps = c
	s.browser, s.version, s.platform, s.labels = c.browser(), c.version(), c.platform(), c.labels()
	if s.browser == "" {
		reply(w, errMsg("browser not set"), http.StatusBadRequest)
		log.Printf("[%d] [%.2fs] [BROWSER_NOT_SET] [%s] [%s] [-] [-] [-] [-] [-]\n", s.id, secondsSince(s.start), s.user, s.remote)
		return
	}
	confLock.RLock()
	s.browsers = quota[s.user]
	confLock.RUnlock()
	s.excluded = newSet()
	var hosts config.Hosts
	hosts, s.version, _ = s.browsers.find(s.browser, s.version, s.platform, s.excluded, newSet())
	if len(hosts) == 0 {
		reply(w, errMsg(fmt.Sprintf("unsupported browser: %s", s.fmtBrowser())), http.StatusNotFound)
		s.finish(resultUnsupported)
		s.log("UNSUPPORTED_BROWSER", "", "", false, "")
		return
	}
	switch s.round(w, r) {
	case roundDone:
	case roundBusy:
		if queueTimeout > 0 {
			s.wait(w, r)
			return
		}
		s.fail(w, http.StatusInternalServerError, "", resultFailed)
	case roundExhausted:
		s.fail(w, http.StatusInternalServerError, "", resultFailed)
	}
}

// candidates returns hosts that may be tried in the current round.
// unavailable is set when some hosts were skipped by the circuit breaker.
func (s *newSession) candidates(excludedHosts set, excludedRegions set) (hosts config.Hosts, regions set, unavailable bool) {
	all, version, regions := s.browsers.find(s.browser, s.version, s.platform, excludedHosts, excludedRegions)
	s.version = version
	for _, h := range all {
		if breakerAvailable(h.Net()) {
			hosts = append(hosts, h)
		} else {
			unavailable = true
		}
	}
	return hosts, regions, unavailable
}

// round tries every host available for the request once, until a session
// is created.
func (s *newSession) round(w http.ResponseWriter, r *http.Request) roundResult {
	excludedHosts := newSet()
	for host := range s.excluded.data {
		excludedHosts.add(host)
	}
	hosts, excludedRegions, unavailable := s.candidates(excludedHosts, newSet())
	busy := unavailable
	for len(hosts) > 0 {
		h, i := pick(hosts)
		if h == nil {
			break
		}
		host := h.Net()
		if !breakerAcquire(host) {
			// Another request is probing this host right now.
			busy = true
			excludedHosts.add(host)
			hosts = append(hosts[:i], hosts[i+1:]...)
			continue
		}
		s.attempts++
		r.Header.Del("X-Selenoid-No-Wait")
		// With a single host left Selenoid may queue the request itself,
		// unless Ggr queues: then hosts must answer at once so that the
		// session goes to whichever host frees a slot first.
		if queueTimeout > 0 || len(hosts) != 1 {
			r.Header.Add("X-Selenoid-No-Wait", "")
		}
		s.log("SESSION_ATTEMPTED", host, "", true, "")
		s.caps.setVersion(s.version)
		metricAttempts.WithLabelValues(host).Inc()
		noteAttempt(host)
		ctx, cancel := attemptContext(r.Context())
		resp, status := session(ctx, h, r.Header, s.caps)
		cancel()
		if r.Context().Err() != nil {
			if status == seleniumError {
				// Possibly aborted by us after orphanGrace: says nothing.
				breakerRelease(host)
			} else {
				breakerSuccess(host)
			}
			s.finish(resultCanceled)
			sess, ok := sessionIDOf(resp)
			if status == browserStarted && ok {
				s.log("CLIENT_DISCONNECTED", host, sess, true, "session created after the client went away")
				s.deleteOrphan(h, sess)
			} else {
				s.log("CLIENT_DISCONNECTED", host, "", true, "")
			}
			return roundDone
		}
		reason := ""
		switch status {
		case browserStarted:
			breakerSuccess(host)
			s.created(w, r, h, resp)
			return roundDone
		case hostBusy:
			breakerSuccess(host)
			reason = reasonBusy
			busy = true
			excludedHosts.add(host)
			hosts = append(hosts[:i], hosts[i+1:]...)
		case browserFailed:
			breakerSuccess(host)
			reason = reasonStartFailed
			s.startFailures++
			s.excluded.add(host)
			excludedHosts.add(host)
			hosts = append(hosts[:i], hosts[i+1:]...)
		case seleniumError:
			breakerFailure(host)
			reason = reasonHostError
			if breakerEnabled() {
				busy = true
			} else {
				s.excluded.add(host)
			}
			excludedHosts.add(host)
			excludedRegions.add(h.Region)
			var skipped bool
			hosts, excludedRegions, skipped = s.candidates(excludedHosts, excludedRegions)
			busy = busy || skipped
		}
		metricFailures.WithLabelValues(host, reason).Inc()
		msg := browserErrMsg(resp)
		s.log("SESSION_FAILED", host, "", true, msg)
		if msg != "" {
			s.lastHostError = msg
		} else if reason == reasonBusy {
			s.lastHostError = "host is busy"
		}
		if reason == reasonStartFailed && maxStartFailures > 0 && s.startFailures >= maxStartFailures {
			s.lastHostError = fmt.Sprintf("%s (gave up after %d browser start failure(s))", dash(s.lastHostError), s.startFailures)
			return roundExhausted
		}
	}
	if busy {
		if s.attempts == 0 && s.lastHostError == "" {
			s.lastHostError = "all hosts are temporarily unavailable"
		}
		return roundBusy
	}
	return roundExhausted
}

// created replies with the session ID prefixed by the host hash.
func (s *newSession) created(w http.ResponseWriter, r *http.Request, h *config.Host, resp map[string]interface{}) {
	sess, ok := sessionIDOf(resp)
	if !ok {
		reply(w, errMsg("protocol error"), http.StatusBadGateway)
		s.finish(resultBadResponse)
		s.log("BAD_RESPONSE", h.Net(), "", false, "")
		return
	}
	if _, ok := resp["sessionId"].(string); ok {
		resp["sessionId"] = h.Sum() + sess
	} else {
		valueMap := resp["value"].(map[string]interface{})
		valueMap["sessionId"] = h.Sum() + sess
		rewriteCDP(r, valueMap, h.Sum()+sess)
	}
	reply(w, resp, http.StatusOK)
	atomic.AddUint64(&numSessions, 1)
	metricSessionsCreated.WithLabelValues(h.Net()).Inc()
	s.finish(resultCreated)
	s.log("SESSION_CREATED", h.Net(), sess, true, "")
}

// fail replies that no session was created.
func (s *newSession) fail(w http.ResponseWriter, code int, reason string, result string) {
	msg := fmt.Sprintf("cannot create session %s on any hosts after %d attempt(s)", s.fmtBrowser(), s.attempts)
	if reason != "" {
		msg = fmt.Sprintf("%s: %s", msg, reason)
	}
	if len(s.lastHostError) > 0 {
		msg = fmt.Sprintf("%s, last host error was: %s", msg, s.lastHostError)
	}
	reply(w, errMsg(msg), code)
	s.finish(result)
	s.log("SESSION_NOT_CREATED", "", "", false, reason)
}

func secondsSince(start time.Time) float64 {
	return time.Since(start).Seconds()
}

func proxy(w http.ResponseWriter, r *http.Request) {
	id := serial()
	_, remote := info(r)
	if len(r.URL.Path) <= tail {
		log.Printf("[%d] [-] [INVALID_URL] [-] [%s] [%s] [-] [-] [-] [-]\n", id, remote, r.URL.Path)
		err(w, r)
		return
	}
	sum := r.URL.Path[head:tail]
	proxyPath := r.URL.Path[:head] + r.URL.Path[tail:]
	confLock.RLock()
	h, ok := routes[sum]
	confLock.RUnlock()
	if !ok {
		log.Printf("[%d] [-] [ROUTE_NOT_FOUND] [-] [%s] [%s] [-] [-] [-] [-]\n", id, remote, proxyPath)
		err(w, r)
		return
	}
	(&httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			if out.Body != nil {
				if body, err := io.ReadAll(out.Body); err == nil {
					_ = out.Body.Close()
					var msg map[string]interface{}
					if err := json.Unmarshal(body, &msg); err == nil {
						delete(msg, "sessionId")
						body, _ = json.Marshal(msg)
						out.ContentLength = int64(len(body))
					}
					out.Body = io.NopCloser(bytes.NewReader(body))
				}
			}
			setForwardedFor(pr)
			setHostAuth(out.Header, h)
			out.URL.Scheme = "http"
			if h.Scheme != "" {
				out.URL.Scheme = h.Scheme
			}
			out.Host = h.Net()
			out.URL.Host = h.Net()
			out.URL.Path = proxyPath
			out.URL.RawPath = ""
			fragments := strings.Split(proxyPath, "/")
			sess := fragments[sessPart]
			if verbose {
				log.Printf("[%d] [-] [PROXYING] [-] [%s] [-] [%s] [%s] [-] [%s]\n", id, remote, h.Net(), sess, proxyPath)
			}
			if out.Method == http.MethodDelete && len(fragments) == sessPart+1 {
				log.Printf("[%d] [-] [SESSION_DELETED] [-] [%s] [-] [%s] [%s] [-] [-]\n", id, remote, h.Net(), sess)
			}
		},
		ErrorHandler: defaultErrorHandler(id),
	}).ServeHTTP(w, r)
}

// setForwardedFor appends the client address to X-Forwarded-For the same way
// ReverseProxy.Director did: Rewrite strips the inbound header otherwise.
func setForwardedFor(pr *httputil.ProxyRequest) {
	clientIP, _, err := net.SplitHostPort(pr.In.RemoteAddr)
	if err != nil {
		return
	}
	if prior := pr.In.Header["X-Forwarded-For"]; len(prior) > 0 {
		clientIP = strings.Join(prior, ", ") + ", " + clientIP
	}
	pr.Out.Header.Set("X-Forwarded-For", clientIP)
}

func ping(w http.ResponseWriter, _ *http.Request) {
	confLock.RLock()
	lrt := lastReloadTime.Format(time.RFC3339)
	confLock.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Uptime         string `json:"uptime"`
		LastReloadTime string `json:"lastReloadTime"`
		NumRequests    uint64 `json:"numRequests"`
		NumSessions    uint64 `json:"numSessions"`
		Version        string `json:"version"`
	}{
		time.Since(startTime).String(),
		lrt,
		atomic.LoadUint64(&numRequests),
		atomic.LoadUint64(&numSessions),
		gitRevision,
	})
}

func status(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(
		map[string]interface{}{
			"value": map[string]interface{}{
				"message": fmt.Sprintf("Ggr %s built at %s", gitRevision, buildStamp),
				"ready":   true,
			},
		})
}

func err(w http.ResponseWriter, _ *http.Request) {
	reply(w, errMsg("route not found"), http.StatusNotFound)
}

func host(w http.ResponseWriter, r *http.Request) {
	id := serial()
	user, remote := info(r)
	head := len(paths.Host)
	tail := head + md5SumLength
	path := r.URL.Path
	if len(path) < tail {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid session ID"))
		return
	}
	sum := path[head:tail]
	confLock.RLock()
	h, ok := routes[sum]
	confLock.RUnlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("unknown host"))
		return
	}
	log.Printf("[%d] [-] [HOST_INFO_REQUESTED] [%s] [%s] [-] [%s] [%s] [-] [-]\n", id, user, remote, h.Name, sum)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(config.Host{Name: h.Name, Port: h.Port, Count: h.Count})
}

func quotaInfo(w http.ResponseWriter, r *http.Request) {
	id := serial()
	user, remote := info(r)
	log.Printf("[%d] [-] [QUOTA_INFO_REQUESTED] [%s] [%s] [-] [-] [-] [-] [-]\n", id, user, remote)
	confLock.RLock()
	browsers := withoutCredentials(quota[user].Browsers.Browsers)
	confLock.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(browsers)
}

// withoutCredentials returns a deep copy of browsers with host credentials
// removed. Loaded quota shares host structs with routes, so it must never be
// modified in place.
func withoutCredentials(browsers []config.Browser) []config.Browser {
	if browsers == nil {
		return nil
	}
	ret := make([]config.Browser, len(browsers))
	for i, b := range browsers {
		b.Versions = append([]config.Version(nil), b.Versions...)
		for j, v := range b.Versions {
			v.Regions = append([]config.Region(nil), v.Regions...)
			for k, r := range v.Regions {
				r.Hosts = append(config.Hosts(nil), r.Hosts...)
				for l := range r.Hosts {
					r.Hosts[l].Username = ""
					r.Hosts[l].Password = ""
				}
				v.Regions[k] = r
			}
			b.Versions[j] = v
		}
		ret[i] = b
	}
	return ret
}

func postOnly(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			reply(w, errMsg("method not allowed"), http.StatusMethodNotAllowed)
			return
		}
		handler(w, r)
	}
}

func readConfig(fn string, browsers *config.Browsers) error {
	file, err := os.ReadFile(fn)
	if err != nil {
		return fmt.Errorf("error reading configuration file %s: %v", fn, err)
	}
	if err := xml.Unmarshal(file, browsers); err != nil {
		return fmt.Errorf("error parsing configuration file %s: %v", fn, err)
	}
	return nil
}

func appendRoutes(routes Routes, browsers *config.Browsers) Routes {
	for _, b := range browsers.Browsers {
		for _, v := range b.Versions {
			for _, r := range v.Regions {
				for i, h := range r.Hosts {
					// It is important to use the r.Hosts[i] here!
					r.Hosts[i].Region = r.Name
					r.Hosts[i].VncInfo = createVNCInfo(h)
					routes[h.Sum()] = &r.Hosts[i]
				}
			}
		}
	}
	return routes
}

func createVNCInfo(h config.Host) *config.VncInfo {
	vncURL := h.VNC
	if vncURL != "" {
		u, err := url.Parse(vncURL)
		if err != nil {
			log.Printf("[-] [-] [INVALID_HOST_VNC_URL] [-] [-] [%s] [%s] [-] [-] [-]\n", vncURL, fmt.Sprintf("%s:%d", h.Name, h.Port))
			return nil
		}
		if u.Scheme != vncScheme && u.Scheme != wsScheme && u.Scheme != wssScheme {
			log.Printf("[-] [-] [UNSUPPORTED_HOST_VNC_SCHEME] [-] [-] [%s] [%s] [-] [-] [-]\n", vncURL, fmt.Sprintf("%s:%d", h.Name, h.Port))
			return nil
		}
		vncInfo := config.VncInfo{
			Scheme: u.Scheme,
			Path:   u.Path,
		}
		vncInfo.Host, vncInfo.Port = u.Hostname(), u.Port()
		if vncInfo.Port == "" {
			vncInfo.Port = map[string]string{vncScheme: defaultVNCPort, wsScheme: "80", wssScheme: "443"}[u.Scheme]
		}
		return &vncInfo
	}
	return nil
}

func requireBasicAuth(authenticator *auth.BasicAuth, handler func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if authenticator.CheckAuth(r) == "" {
			authenticator.RequireAuth(w, r)
			return
		}
		handler(w, r)
	}
}

// WithSuitableAuthentication handles basic authentication and guest quota processing
func WithSuitableAuthentication(authenticator *auth.BasicAuth, handler func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if rootToken != "" {
			if rootToken == r.Header.Get("X-Ggr-Root-Token") {
				handler(w, r)
				return
			}
		}
		if !guestAccessAllowed {
			//All requests require authentication
			requireBasicAuth(authenticator, handler)(w, r)
		} else if _, _, basicAuthPresent := r.BasicAuth(); !basicAuthPresent {
			//Run the handler as unauthenticated user
			confLock.RLock()
			_, ok := quota[guestUserName]
			confLock.RUnlock()
			if !ok {
				reply(w, errMsg("Guest access is unavailable"), http.StatusUnauthorized)
			} else {
				handler(w, r)
			}
		} else {
			//Run the handler using basic authentication
			if fileExists(users) {
				requireBasicAuth(authenticator, handler)(w, r)
			} else {
				handler(w, r)
			}
		}
	}
}

func vnc(wsconn *websocket.Conn) {
	defer wsconn.Close()

	id := serial()
	head := len(paths.VNC)
	tail := head + md5SumLength
	path := wsconn.Request().URL.Path
	if len(path) < tail {
		log.Printf("[%d] [-] [INVALID_VNC_REQUEST_URL] [-] [-] [%s] [-] [-] [-] [-]\n", id, path)
		return
	}
	sum := path[head:tail]
	confLock.RLock()
	h, ok := routes[sum]
	confLock.RUnlock()
	if ok {
		vncInfo := h.VncInfo
		scheme := vncScheme
		host := h.Name
		port := defaultVNCPort
		path := ""
		if vncInfo != nil {
			scheme = vncInfo.Scheme
			host = vncInfo.Host
			port = vncInfo.Port
			path = vncInfo.Path
		}
		sessionID := strings.Split(wsconn.Request().URL.Path, "/")[2][md5SumLength:]
		switch scheme {
		case vncScheme:
			proxyVNC(id, wsconn, sessionID, host, port)
		case wsScheme, wssScheme:
			proxyWebSockets(id, wsconn, h, sessionID, scheme, host, port, path)
		default:
			{
				log.Printf("[%d] [-] [UNSUPPORTED_HOST_VNC_SCHEME] [-] [-] [%s] [-] [-] [-] [-]\n", id, scheme)
				return
			}
		}
	} else {
		log.Printf("[%d] [-] [UNKNOWN_VNC_HOST] [-] [-] [-] [-] [%s] [-] [-]\n", id, sum)
	}

}

func proxyVNC(id uint64, wsconn *websocket.Conn, sessionID string, host string, port string) {
	var d net.Dialer
	address := fmt.Sprintf("%s:%s", host, port)
	conn, err := d.DialContext(wsconn.Request().Context(), "tcp", address)
	proxyConn(id, wsconn, conn, err, sessionID, address)
}

func proxyWebSockets(id uint64, wsconn *websocket.Conn, h *config.Host, sessionID string, scheme string, host string, port string, path string) {
	origin := "http://localhost/"
	u := fmt.Sprintf("%s://%s%s/%s", scheme, net.JoinHostPort(host, port), path, sessionID)
	cfg, err := websocket.NewConfig(u, origin)
	if err != nil {
		proxyConn(id, wsconn, nil, err, sessionID, u)
		return
	}
	setHostAuth(cfg.Header, h)
	conn, err := cfg.DialContext(wsconn.Request().Context())
	proxyConn(id, wsconn, conn, err, sessionID, u)
}

func proxyConn(id uint64, wsconn *websocket.Conn, conn net.Conn, err error, sessionID string, address string) {
	log.Printf("[%d] [-] [PROXYING_TO_WS] [-] [-] [-] [%s] [%s] [-] [-]", id, address, sessionID)
	if err != nil {
		log.Printf("[%d] [-] [WS_ERROR] [-] [-] [-] [%s] [%s] [-] [%v]", id, sessionID, address, err)
		return
	}
	defer conn.Close()
	wsconn.PayloadType = websocket.BinaryFrame
	go func() {
		io.Copy(wsconn, conn)
		_ = wsconn.Close()
		log.Printf("[%d] [-] [WS_SESSION_CLOSED] [-] [-] [-] [%s] [%s] [-] [-]", id, address, sessionID)
	}()
	_, _ = io.Copy(conn, wsconn)
	log.Printf("[%d] [-] [WS_CLIENT_DISCONNECTED] [-] [-] [-] [%s] [%s] [-] [-]", id, address, sessionID)
}

func devtools(w http.ResponseWriter, r *http.Request) {
	proxyStatic(w, r, paths.Devtools, "INVALID_DEVTOOLS_REQUEST_URL", "PROXYING_DEVTOOLS", "UNKNOWN_DEVTOOLS_HOST", func(remainder string) string {
		return fmt.Sprintf("/devtools/%s", remainder)
	})
}

func video(w http.ResponseWriter, r *http.Request) {
	proxyStatic(w, r, paths.Video, "INVALID_VIDEO_REQUEST_URL", "PROXYING_VIDEO", "UNKNOWN_VIDEO_HOST", func(sessionId string) string {
		return fmt.Sprintf("/video/%s.mp4", sessionId)
	})
}

func logs(w http.ResponseWriter, r *http.Request) {
	proxyStatic(w, r, paths.Logs, "INVALID_LOG_REQUEST_URL", "PROXYING_LOG", "UNKNOWN_LOG_HOST", func(sessionId string) string {
		return fmt.Sprintf("/logs/%s.log", sessionId)
	})
}

func download(w http.ResponseWriter, r *http.Request) {
	proxyStatic(w, r, paths.Download, "INVALID_DOWNLOAD_REQUEST_URL", "PROXYING_DOWNLOAD", "UNKNOWN_DOWNLOAD_HOST", func(remainder string) string {
		return fmt.Sprintf("/download/%s", remainder)
	})
}

func clipboard(w http.ResponseWriter, r *http.Request) {
	proxyStatic(w, r, paths.Clipboard, "INVALID_CLIPBOARD_REQUEST_URL", "PROXYING_CLIPBOARD", "UNKNOWN_DOWNLOAD_HOST", func(remainder string) string {
		return fmt.Sprintf("/clipboard/%s", remainder)
	})
}

func proxyStatic(w http.ResponseWriter, r *http.Request, route string, invalidUrlMessage string, proxyingMessage string, unknownHostMessage string, pathProvider func(string) string) {
	id := serial()
	user, remote := info(r)
	head := len(route)
	tail := head + md5SumLength
	path := r.URL.Path
	if len(path) < tail {
		log.Printf("[%d] [-] [%s] [%s] [%s] [%s] [-] [-] [-] [-]\n", id, invalidUrlMessage, user, remote, path)
		reply(w, errMsg("invalid request URL"), http.StatusNotFound)
		return
	}
	sum := path[head:tail]
	confLock.RLock()
	h, ok := routes[sum]
	confLock.RUnlock()
	remainder := path[tail:]
	if ok {
		(&httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				setForwardedFor(pr)
				setHostAuth(pr.Out.Header, h)
				out := pr.Out
				out.URL.Scheme = "http"
				if h.Scheme != "" {
					out.URL.Scheme = h.Scheme
				}
				out.URL.Host = h.Net()
				out.URL.Path = pathProvider(remainder)
				out.URL.RawPath = ""
				log.Printf("[%d] [-] [%s] [%s] [%s] [%s] [-] [%s] [-] [-]\n", id, proxyingMessage, user, remote, out.URL, remainder)
			},
			ErrorHandler: defaultErrorHandler(id),
		}).ServeHTTP(w, r)
	} else {
		log.Printf("[%d] [-] [%s] [%s] [%s] [-] [-] [%s] [-] [-]\n", id, unknownHostMessage, user, remote, sum)
		reply(w, errMsg("unknown host"), http.StatusNotFound)
	}
}

func defaultErrorHandler(requestId uint64) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		user, remote := info(r)
		log.Printf("[%d] [-] [PROXY_ERROR] [%s] [%s] [%s] [-] [-] [-] [%v]", requestId, user, remote, r.URL, err)
		w.WriteHeader(http.StatusBadGateway)
	}
}

func mux() http.Handler {
	mux := http.NewServeMux()
	authenticator := &auth.BasicAuth{
		Realm:   "Selenium Grid Router",
		Secrets: auth.HtpasswdFileProvider(users),
	}
	mux.HandleFunc(paths.Ping, ping)
	mux.HandleFunc(paths.Status, status)
	mux.HandleFunc(paths.Err, err)
	mux.HandleFunc(paths.Host, WithSuitableAuthentication(authenticator, host))
	mux.HandleFunc(paths.Quota, WithSuitableAuthentication(authenticator, quotaInfo))
	mux.HandleFunc(paths.Route, WithSuitableAuthentication(authenticator, postOnly(route)))
	mux.HandleFunc(paths.Proxy, proxy)
	mux.Handle(paths.VNC, websocket.Handler(vnc))
	mux.HandleFunc(paths.Video, WithSuitableAuthentication(authenticator, video))
	mux.HandleFunc(paths.Logs, WithSuitableAuthentication(authenticator, logs))
	mux.HandleFunc(paths.Download, WithSuitableAuthentication(authenticator, download))
	mux.HandleFunc(paths.Clipboard, WithSuitableAuthentication(authenticator, clipboard))
	mux.HandleFunc(paths.Devtools, devtools)
	mux.Handle(paths.Pprof, http.DefaultServeMux)
	mux.Handle(paths.Metrics, promhttp.Handler())
	return mux
}
