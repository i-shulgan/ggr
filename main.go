package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/aerokube/ggr/config"
)

var (
	listen             = ":4444"
	quotaDir           = "quota"
	users              = ".htpasswd"
	timeout            = 300 * time.Second
	gracefulPeriod     = 300 * time.Second
	guestAccessAllowed bool
	guestUserName      = "guest"
	rootToken          string
	verbose            bool

	startTime      = time.Now()
	lastReloadTime = time.Now()

	version     bool
	gitRevision = "HEAD"
	buildStamp  = "unknown"

	// startHooks run after flags are parsed and quota is loaded, e.g. the
	// quota directory watcher in the "watch" build.
	startHooks []func()
)

func loadQuotaFiles(quotaDir string) error {
	log.Printf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [Loading configuration files from \"%s\"]\n", quotaDir)

	glob := fmt.Sprintf("%s%c%s", quotaDir, filepath.Separator, "*.xml")
	files, _ := filepath.Glob(glob)
	if len(files) == 0 {
		return fmt.Errorf("no quota XML files found in [%s] - exiting", quotaDir)
	}

	for _, file := range files {
		loadQuotaFile(file)
	}
	return nil
}

func loadQuotaFile(file string) {
	fileName := filepath.Base(file)
	quotaName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	var browsers Browsers
	err := readConfig(file, &browsers)
	if err != nil {
		log.Printf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [Failed to load configuration from \"%s\": %v]\n", fileName, err)
		return
	}
	updateQuota(quotaName, browsers)
	if verbose {
		log.Printf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [Loaded configuration from \"%s\"]\n%v\n", file, browsers)
	}
}

func updateQuota(quotaName string, browsers Browsers) {
	confLock.Lock()
	defer confLock.Unlock()
	quota[quotaName] = ggrBrowsers{browsers}
	routes = appendRoutes(routes, &browsers)
	lastReloadTime = time.Now()
}

func showVersion() {
	fmt.Printf("Git Revision: %s\n", gitRevision)
	fmt.Printf("UTC Build Time: %s\n", buildStamp)
}

func fileExists(p string) bool {
	fileInfo, err := os.Stat(p)
	return !os.IsNotExist(err) && !fileInfo.IsDir()
}

func parseFlags() {
	flag.BoolVar(&guestAccessAllowed, "guests-allowed", guestAccessAllowed, "Allow guest (unauthenticated) users to access the grid")
	flag.StringVar(&guestUserName, "guests-quota", guestUserName, "Which quota file to use for guests")
	flag.StringVar(&listen, "listen", listen, "host and port to listen to")
	flag.StringVar(&quotaDir, "quotaDir", quotaDir, "quota directory")
	flag.StringVar(&users, "users", users, "htpasswd auth file path")
	flag.DurationVar(&timeout, "timeout", timeout, "session creation timeout in time.Duration format, e.g. 300s or 500ms")
	flag.DurationVar(&gracefulPeriod, "graceful-period", gracefulPeriod, "graceful shutdown period in time.Duration format, e.g. 300s or 500ms")
	flag.StringVar(&rootToken, "root-token", rootToken, "Root token value")
	flag.BoolVar(&version, "version", version, "show version and exit")
	flag.BoolVar(&verbose, "verbose", verbose, "enable verbose mode")
	flag.Parse()
}

func configure() {
	log.Printf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [Users file is \"%s\"]\n", users)
	if !fileExists(users) && !guestAccessAllowed {
		log.Fatalf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [Users file \"%s\" does not exist or is a directory]\n", users)
	}
	if err := loadQuotaFiles(quotaDir); err != nil {
		log.Fatalf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [%v]\n", err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		for range sig {
			err := loadQuotaFiles(quotaDir)
			if err != nil {
				log.Printf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [%v]\n", err)
			}
		}
	}()
	for _, hook := range startHooks {
		hook()
	}
}

func main() {
	parseFlags()
	if version {
		showVersion()
		os.Exit(0)
	}
	configure()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	server := &http.Server{
		Addr:    listen,
		Handler: mux(),
	}
	e := make(chan error, 1)
	go func() {
		e <- server.ListenAndServe()
	}()
	select {
	case err := <-e:
		log.Fatalf("[-] [-] [INIT] [-] [-] [-] [-] [-] [-] [%v]\n", err)
	case <-stop:
	}

	log.Printf("[-] [%s] [SHUTTING_DOWN] [-] [-] [-] [-] [-] [-] [-]\n", gracefulPeriod)
	ctx, cancel := context.WithTimeout(context.Background(), gracefulPeriod)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("[-] [-] [SHUTDOWN_FAILURE] [-] [-] [-] [-] [-] [-] [%v]\n", err)
	}
}
