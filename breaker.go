package main

import (
	"log"
	"sync"
	"time"
)

var (
	// breakerTimeout is how long a host stays closed for new sessions after
	// breakerThreshold consecutive connection errors. Zero disables the
	// breaker: like upstream, a failed host is then only skipped for the rest
	// of the current request.
	breakerTimeout   time.Duration
	breakerThreshold = 1

	breakers = struct {
		sync.Mutex
		m map[string]*breaker
	}{m: make(map[string]*breaker)}
)

// breaker is the state of a host shared by all requests. It is open while
// failures >= breakerThreshold and openUntil is in the future. After that a
// single request (probe) is let through: success closes the breaker, a
// failure opens it again.
type breaker struct {
	failures  int
	openUntil time.Time
	probing   bool
}

func breakerEnabled() bool {
	return breakerTimeout > 0
}

func (b *breaker) tripped() bool {
	return b != nil && b.failures >= breakerThreshold
}

// breakerAvailable reports whether a host may be tried now without changing
// its state.
func breakerAvailable(host string) bool {
	if !breakerEnabled() {
		return true
	}
	breakers.Lock()
	defer breakers.Unlock()
	b := breakers.m[host]
	if !b.tripped() {
		return true
	}
	return !b.probing && !time.Now().Before(b.openUntil)
}

// breakerAcquire must succeed before an attempt on a host. When the open
// period is over only one attempt at a time is allowed. Every acquired
// attempt ends with breakerSuccess, breakerFailure or breakerRelease.
func breakerAcquire(host string) bool {
	if !breakerEnabled() {
		return true
	}
	breakers.Lock()
	defer breakers.Unlock()
	b := breakers.m[host]
	if !b.tripped() {
		return true
	}
	if b.probing || time.Now().Before(b.openUntil) {
		return false
	}
	b.probing = true
	return true
}

// breakerSuccess records that the host answered.
func breakerSuccess(host string) {
	if !breakerEnabled() {
		return
	}
	breakers.Lock()
	defer breakers.Unlock()
	b, ok := breakers.m[host]
	if !ok {
		return
	}
	if b.tripped() {
		log.Printf("[-] [-] [BREAKER_CLOSED] [-] [-] [-] [%s] [-] [-] [-]\n", host)
		metricBreakerOpen.WithLabelValues(host).Set(0)
	}
	delete(breakers.m, host)
}

// breakerFailure records a connection error or an unparsable answer.
func breakerFailure(host string) {
	if !breakerEnabled() {
		return
	}
	breakers.Lock()
	defer breakers.Unlock()
	b, ok := breakers.m[host]
	if !ok {
		b = &breaker{}
		breakers.m[host] = b
	}
	b.failures++
	b.probing = false
	if b.tripped() {
		b.openUntil = time.Now().Add(breakerTimeout)
		log.Printf("[-] [-] [BREAKER_OPEN] [-] [-] [-] [%s] [-] [%d] [%s]\n", host, b.failures, breakerTimeout)
		metricBreakerOpen.WithLabelValues(host).Set(1)
	}
}

// breakerRelease ends an attempt that says nothing about the host, e.g. when
// the client went away.
func breakerRelease(host string) {
	if !breakerEnabled() {
		return
	}
	breakers.Lock()
	defer breakers.Unlock()
	if b, ok := breakers.m[host]; ok {
		b.probing = false
	}
}

// resetBreakers forgets all breaker state.
func resetBreakers() {
	breakers.Lock()
	defer breakers.Unlock()
	for host := range breakers.m {
		metricBreakerOpen.WithLabelValues(host).Set(0)
	}
	breakers.m = make(map[string]*breaker)
}
