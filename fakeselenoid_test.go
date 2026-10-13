package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aerokube/ggr/config"
)

// fakeSelenoid imitates the parts of Selenoid that Ggr talks to: a limited
// number of slots, X-Selenoid-No-Wait, browser start errors, /status and
// session deletion.
type fakeSelenoid struct {
	*httptest.Server
	host config.Host

	mu        sync.Mutex
	total     int
	used      map[string]bool
	failStart bool
	attempts  int
	noWait    int
	deleted   []string
	// onCreate, when set, runs before a created session is returned.
	onCreate func(r *http.Request)
	nextID   int
	freed    chan struct{}
}

func newFakeSelenoid(t *testing.T, total int) *fakeSelenoid {
	f := &fakeSelenoid{total: total, used: map[string]bool{}, freed: make(chan struct{}, 100)}
	mux := http.NewServeMux()
	mux.HandleFunc("/wd/hub/session", f.create)
	mux.HandleFunc("/wd/hub/session/", f.delete)
	mux.HandleFunc("/status", f.status)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	host, port := hostportnum(f.URL)
	f.host = config.Host{Name: host, Port: port, Count: 1, Region: "1"}
	return f
}

func jsonError(w http.ResponseWriter, code int, err, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"value": map[string]string{"error": err, "message": msg}})
}

func (f *fakeSelenoid) create(w http.ResponseWriter, r *http.Request) {
	_, noWait := r.Header["X-Selenoid-No-Wait"]
	f.mu.Lock()
	f.attempts++
	if noWait {
		f.noWait++
	}
	if f.failStart {
		f.mu.Unlock()
		jsonError(w, http.StatusInternalServerError, "session not created", "failed to start browser")
		return
	}
	if len(f.used) >= f.total {
		f.mu.Unlock()
		// Selenoid encodes this as an unknown error with status 500.
		jsonError(w, http.StatusInternalServerError, "unknown error", "Too Many Requests")
		return
	}
	f.nextID++
	id := fmt.Sprintf("s%d", f.nextID)
	f.used[id] = true
	onCreate := f.onCreate
	f.mu.Unlock()
	if onCreate != nil {
		onCreate(r)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"value": map[string]interface{}{"sessionId": id, "capabilities": map[string]interface{}{}}})
}

func (f *fakeSelenoid) delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/wd/hub/session/")
	f.release(id)
	f.mu.Lock()
	f.deleted = append(f.deleted, id)
	f.mu.Unlock()
}

func (f *fakeSelenoid) release(id string) {
	f.mu.Lock()
	delete(f.used, id)
	f.mu.Unlock()
	f.freed <- struct{}{}
}

func (f *fakeSelenoid) status(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]int{"total": f.total, "used": len(f.used), "pending": 0, "queued": 0})
}

func (f *fakeSelenoid) setFailStart(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStart = v
}

func (f *fakeSelenoid) counters() (attempts, noWait int, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, f.noWait, append([]string(nil), f.deleted...)
}

func (f *fakeSelenoid) usedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.used {
		ids = append(ids, id)
	}
	return ids
}

// quotaOf builds a quota with one region per host.
func quotaOf(hosts ...config.Host) config.Browsers {
	var regions []config.Region
	for i, h := range hosts {
		regions = append(regions, config.Region{Name: fmt.Sprint(i), Hosts: config.Hosts{h}})
	}
	return config.Browsers{Browsers: []config.Browser{
		{Name: "browser", DefaultVersion: "1.0", Versions: []config.Version{
			{Number: "1.0", Regions: regions},
		}}}}
}

// deadHost returns a host that refuses connections.
func deadHost(t *testing.T) config.Host {
	s := httptest.NewServer(http.NotFoundHandler())
	host, port := hostportnum(s.URL)
	s.Close()
	return config.Host{Name: host, Port: port, Count: 1}
}

const w3cCaps = `{"capabilities":{"alwaysMatch":{"browserName":"browser"}}}`

// setGlobal sets a package variable for the duration of the test.
func setGlobal[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}
