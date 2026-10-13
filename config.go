package main

import (
	"github.com/aerokube/ggr/config"
	"math/rand"
	"strings"
)

type set interface {
	contains(el string) bool
	add(el string)
	size() int
}

func newSet(data ...string) *setImpl {
	set := &setImpl{make(map[string]struct{})}
	for _, el := range data {
		set.add(el)
	}
	return set
}

type setImpl struct {
	data map[string]struct{}
}

func (ss *setImpl) contains(el string) bool {
	_, ok := ss.data[el]
	return ok
}

func (ss *setImpl) add(el string) {
	ss.data[el] = struct{}{}
}

func (ss *setImpl) size() int {
	return len(ss.data)
}

func sessionURL(h *config.Host) string {
	return h.Route() + paths.Route
}

type ggrBrowsers struct {
	config.Browsers
}

const anyPlatform = "ANY"

func (b *ggrBrowsers) find(browser, version string, platform string, excludedHosts set, excludedRegions set) (config.Hosts, string, set) {
	var hosts config.Hosts
	for _, b := range b.Browsers.Browsers {
		if b.Name == browser {
			if version == "" {
				version = b.DefaultVersion
			}
			if platform == "" || platform == anyPlatform {
				platform = b.DefaultPlatform
			}
			matched := false
			for _, v := range b.Versions {
				// Once a prefix matched, version holds the full number and only
				// exactly equal entries may add hosts: "1" must not match both
				// "10" and "100" while sending "100" to hosts of "10".
				versionMatches := strings.HasPrefix(v.Number, version)
				if matched {
					versionMatches = v.Number == version
				}
				if versionMatches && (v.Platform == "" || strings.HasPrefix(strings.ToLower(v.Platform), strings.ToLower(platform))) {
					matched = true
					version = v.Number
				next:
					for _, r := range v.Regions {
						if excludedRegions.size() == len(v.Regions) {
							excludedRegions = newSet()
						}
						if excludedRegions.contains(r.Name) {
							continue next
						}
						for _, h := range r.Hosts {
							if !excludedHosts.contains(h.Net()) {
								hosts = append(hosts, h)
							}
						}
					}
				}
			}
		}
	}
	return hosts, version, excludedRegions
}

func choose(hosts config.Hosts) (*config.Host, int) {
	total := 0
	for _, h := range hosts {
		total += h.Count
	}
	if total > 0 {
		r := rand.Intn(total)
		for i, host := range hosts {
			r -= host.Count
			if r < 0 {
				return &hosts[i], i
			}
		}
	}
	return nil, -1
}
