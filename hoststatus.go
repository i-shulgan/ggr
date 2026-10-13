package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/aerokube/ggr/config"
)

// statusInterval is how often /status of every host is polled. Zero
// disables polling: hosts are chosen at random weighted by count, as in
// upstream.
var statusInterval time.Duration

// hostStatus is the last known state of a Selenoid host.
type hostStatus struct {
	total, used, pending int
	// assigned counts attempts sent to the host since the last poll, so that
	// requests do not all rush to the same host between polls.
	assigned int
	updated  time.Time
}

var hostStatuses = struct {
	sync.Mutex
	m map[string]*hostStatus
}{m: make(map[string]*hostStatus)}

// selenoidStatus is the part of Selenoid /status that matters here.
type selenoidStatus struct {
	Total   *int `json:"total"`
	Used    int  `json:"used"`
	Pending int  `json:"pending"`
}

func fetchHostStatus(ctx context.Context, h config.Host) (*hostStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.Route()+"/status", nil)
	if err != nil {
		return nil, err
	}
	setHostAuth(req.Header, &h)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{resp.Status}
	}
	var st selenoidStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	if st.Total == nil {
		// Not a Selenoid, e.g. Selenium Grid: nothing to count on.
		return nil, &statusError{"no total in /status"}
	}
	return &hostStatus{total: *st.Total, used: st.Used, pending: st.Pending, updated: time.Now()}, nil
}

type statusError struct{ msg string }

func (e *statusError) Error() string { return e.msg }

// quotaHosts returns every host of every quota once.
func quotaHosts() map[string]config.Host {
	confLock.RLock()
	defer confLock.RUnlock()
	hosts := make(map[string]config.Host)
	for _, browsers := range quota {
		for _, b := range browsers.Browsers.Browsers {
			for _, v := range b.Versions {
				for _, r := range v.Regions {
					for _, h := range r.Hosts {
						hosts[h.Net()] = h
					}
				}
			}
		}
	}
	return hosts
}

// pollStatuses refreshes the state of all hosts in parallel.
func pollStatuses(ctx context.Context) {
	hosts := quotaHosts()
	ctx, cancel := context.WithTimeout(ctx, statusInterval)
	defer cancel()
	var wg sync.WaitGroup
	for name, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := fetchHostStatus(ctx, h)
			hostStatuses.Lock()
			defer hostStatuses.Unlock()
			if err != nil {
				if _, known := hostStatuses.m[name]; known && verbose {
					log.Printf("[-] [-] [HOST_STATUS_UNKNOWN] [-] [-] [-] [%s] [-] [-] [%v]\n", name, err)
				}
				delete(hostStatuses.m, name)
				metricFreeSlots.DeleteLabelValues(name)
				return
			}
			hostStatuses.m[name] = st
			metricFreeSlots.WithLabelValues(name).Set(float64(st.total - st.used - st.pending))
		}()
	}
	wg.Wait()
	hostStatuses.Lock()
	defer hostStatuses.Unlock()
	for name := range hostStatuses.m {
		if _, ok := hosts[name]; !ok {
			delete(hostStatuses.m, name)
			metricFreeSlots.DeleteLabelValues(name)
		}
	}
}

// startStatusPoller polls hosts until ctx is done. The returned channel is
// closed when polling stops.
func startStatusPoller(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if statusInterval <= 0 {
		close(done)
		return done
	}
	log.Printf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [Polling host status every %s]\n", statusInterval)
	go func() {
		defer close(done)
		ticker := time.NewTicker(statusInterval)
		defer ticker.Stop()
		for {
			pollStatuses(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

// freeSlots returns total - used - pending of the host minus attempts sent
// since the last poll, if a fresh status is known.
func freeSlots(host string) (int, bool) {
	if statusInterval <= 0 {
		return 0, false
	}
	hostStatuses.Lock()
	defer hostStatuses.Unlock()
	st, ok := hostStatuses.m[host]
	if !ok || time.Since(st.updated) > 3*statusInterval {
		return 0, false
	}
	return st.total - st.used - st.pending - st.assigned, true
}

func noteAttempt(host string) {
	hostStatuses.Lock()
	defer hostStatuses.Unlock()
	if st, ok := hostStatuses.m[host]; ok {
		st.assigned++
	}
}

func resetHostStatuses() {
	hostStatuses.Lock()
	defer hostStatuses.Unlock()
	hostStatuses.m = make(map[string]*hostStatus)
}

// pick chooses the next host to try. Hosts known to have free slots go
// first, chosen at random weighted by free slots; then hosts without a
// known status, weighted by count as in upstream; hosts known to be full
// go last, as the status may be stale. Hosts with count 0 are never chosen.
func pick(hosts config.Hosts) (*config.Host, int) {
	if statusInterval <= 0 {
		return choose(hosts)
	}
	var withFree, unknown, full []int
	var free []int
	for i, h := range hosts {
		if h.Count <= 0 {
			continue
		}
		n, ok := freeSlots(h.Net())
		switch {
		case ok && n > 0:
			withFree = append(withFree, i)
			free = append(free, n)
		case !ok:
			unknown = append(unknown, i)
		default:
			full = append(full, i)
		}
	}
	if i := weighted(withFree, func(j int) int { return free[j] }); i >= 0 {
		return &hosts[i], i
	}
	byCount := func(idx []int) func(int) int {
		return func(j int) int { return hosts[idx[j]].Count }
	}
	if i := weighted(unknown, byCount(unknown)); i >= 0 {
		return &hosts[i], i
	}
	if i := weighted(full, byCount(full)); i >= 0 {
		return &hosts[i], i
	}
	return nil, -1
}

// weighted returns one of idx chosen at random with weight(j) for idx[j],
// or -1 if idx is empty.
func weighted(idx []int, weight func(int) int) int {
	total := 0
	for j := range idx {
		total += weight(j)
	}
	if total <= 0 {
		return -1
	}
	r := rand.N(total)
	for j, i := range idx {
		r -= weight(j)
		if r < 0 {
			return i
		}
	}
	return -1
}
