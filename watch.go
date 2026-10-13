//go:build watch

package main

import (
	"log"
	"time"

	"github.com/aandryashin/reloader"
)

func init() {
	startHooks = append(startHooks, watchQuota)
}

func watchQuota() {
	err := reloader.Watch(quotaDir, func() {
		if err := loadQuotaFiles(quotaDir); err != nil {
			log.Printf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [%v]\n", err)
		}
	}, 5*time.Second)
	if err != nil {
		log.Fatalf("[-] [INIT] [Failed to init watching quota for changes: %v]", err)
	}
	log.Printf("[-] [INIT] [Watching quota directory %s for changes]", quotaDir)
}
