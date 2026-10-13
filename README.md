# Go Grid Router
[![ci](https://github.com/i-shulgan/ggr/actions/workflows/ci.yml/badge.svg)](https://github.com/i-shulgan/ggr/actions/workflows/ci.yml)

Go Grid Router (aka Ggr) is a lightweight active load balancer used to create scalable and highly-available [Selenium](http://seleniumhq.org/) clusters, usually in front of several [Selenoid](https://github.com/aerokube/selenoid) hosts.

This is a maintained fork of [aerokube/ggr](https://github.com/aerokube/ggr), which is archived since 1.7.2. Its main goal is reliable session creation with several hosts: test suites without client-side retries should not fail just because every slot was taken for a couple of seconds.

![Ggr Animation](docs/img/ggr-animation.gif)

## Differences from upstream

All new behaviour is off by default: with no new flags Ggr routes sessions exactly like upstream 1.7.2, apart from the fixes below.

### Session creation

How upstream creates a session: it picks a host at random weighted by `count`, sends `X-Selenoid-No-Wait` while more than one candidate is left, and removes every host that is busy or fails from the rest of the request. The last remaining host is asked without `X-Selenoid-No-Wait`, so the request either waits in that particular Selenoid's queue (even if another host frees a slot first) or, with `-disable-queue` in Selenoid, fails at once with 500.

What this fork adds:

* **Queue in Ggr** (`-queue-timeout`, `-max-queue`). When every host is busy, the request is not failed: Ggr repeats the pass over all hosts with exponential backoff and jitter until a host accepts it, the timeout expires (500, `queue timeout ... exceeded`) or the client goes away. While queueing, `X-Selenoid-No-Wait` is always sent, so the session goes to whichever host frees a slot first. Above `-max-queue` waiting requests Ggr answers `503 Service Unavailable` with `Retry-After`.
* **Host choice by free slots** (`-status-interval`). Ggr polls `/status` of every quota host in the background and computes `free = total - used - pending` minus attempts it sent since the last poll. Hosts with free slots are tried first, chosen at random weighted by free slots; hosts without a fresh status (down, not Selenoid, last poll older than three intervals) fall back to the upstream random choice weighted by `count`; hosts known to be full are tried last. `count="0"` still disables a host.
* **Different reaction to different failures**:
  * *busy* (`Too Many Requests` or `queue is full` from Selenoid, HTTP 429): the host is skipped only for the current pass and is tried again on the next one;
  * *browser start error* (any other error from the host): the host is not tried again in this request, and `-max-start-failures` limits how many hosts one request tries;
  * *host error* (connection refused, timeout, response that is not JSON): with `-breaker-timeout` a circuit breaker shared by all requests closes the host for new sessions after `-breaker-threshold` consecutive errors, then lets a single probe through to check whether it is back.
* **No orphan sessions.** If the client disconnects while a session is being created, the attempt goes on for up to 30 seconds and a session created meanwhile is deleted with `DELETE /wd/hub/session/<id>` on the host. Upstream left such sessions running until the Selenoid idle timeout.
* **Prometheus metrics** at `/metrics`.

### Fixes

* `GET /quota` wiped `username` and `password` of hosts in the loaded quota itself (and raced with routing), so sessions on hosts behind basic auth failed after anybody requested `/quota` until the next reload.
* Host credentials from quota are now sent not only for new sessions but also for session commands, `/video/`, `/logs/`, `/download/`, `/clipboard/`, `/devtools/`, `/status` polling and websocket VNC. VNC URLs accept `wss://` and default ports.
* The Selenium 4 `se:cdp` capability returned by Selenoid pointed directly to the Selenoid host with its own session ID; it now points to `/devtools/` of Ggr ([aerokube/ggr#362](https://github.com/aerokube/ggr/issues/362)).
* After the client disconnected, the session handler kept writing to a response that `net/http` had already finished (`withCloseNotifier`).
* Quota reload (`SIGHUP` or the `watch` build) did not remove users whose quota files were deleted.
* A version requested by prefix could mix hosts of different versions: with versions `10` and `100` a request for `1` sent `100` to hosts of `10`.
* Flags were parsed and quota loaded in `init()`; signals could be dropped (unbuffered `signal.Notify` channels). `go vet` and `staticcheck` are clean.

### Build and delivery

* Go 1.27.2, dependencies updated, `govulncheck` in CI.
* Multi-stage `Dockerfile`: static binary, `alpine` runtime, runs as `nobody` (uid/gid 65534).
* Images are published to `ghcr.io/i-shulgan/ggr` instead of Docker Hub. No binary release assets and no gh-pages docs.
* Automatic semantic versioning with release-please and Conventional Commits, as in [i-shulgan/ggr-ui](https://github.com/i-shulgan/ggr-ui).

## New flags

| Flag | Default | Description |
|------|---------|-------------|
| `-queue-timeout` | `0` (off) | How long a new session request may wait for a free host when all hosts are busy. |
| `-max-queue` | `0` (unlimited) | Maximum number of waiting requests; the rest get `503` with `Retry-After`. |
| `-queue-backoff` | `250ms` | Delay before the second pass over busy hosts; doubled on every pass. |
| `-queue-backoff-max` | `5s` | Maximum delay between passes. Also the `Retry-After` value, rounded up to seconds. |
| `-status-interval` | `0` (off) | How often to poll `/status` of hosts and prefer hosts with free slots. |
| `-max-start-failures` | `0` (all hosts) | Give up a request after this many browser start errors on different hosts. |
| `-breaker-timeout` | `0` (off) | How long a host gets no new sessions after it stopped answering. Off: the host is skipped for the current request only. |
| `-breaker-threshold` | `1` | Consecutive connection errors that open the circuit breaker of a host. |

The other flags are unchanged, see [docs/cli-flags.adoc](docs/cli-flags.adoc) or `ggr --help`. `-timeout` still limits a single attempt on one host; `-queue-timeout` limits the whole wait.

A configuration for test suites without retries:

```bash
ggr -listen :4444 -users /etc/grid-router/users.htpasswd -quotaDir /etc/grid-router/quota \
    -queue-timeout 5m -max-queue 500 \
    -status-interval 5s \
    -max-start-failures 3 \
    -breaker-timeout 30s -breaker-threshold 3
```

Set the client's new session timeout above `-queue-timeout`, and the timeouts of any proxy in front of Ggr as well.

## Metrics

`/metrics` is served without authentication, like `/ping`.

| Metric | Labels | Meaning |
|--------|--------|---------|
| `ggr_session_attempts_total` | `host` | Attempts to create a session on a host. |
| `ggr_session_failures_total` | `host`, `reason` | Failed attempts: `busy`, `start_failed`, `host_error`. |
| `ggr_sessions_created_total` | `host` | Created sessions. |
| `ggr_session_requests_total` | `result` | New session requests: `created`, `failed`, `queue_timeout`, `queue_full`, `canceled`, `unsupported`, `bad_response`. |
| `ggr_queue_length` | | Requests waiting for a free host. |
| `ggr_queue_wait_seconds` | `result` | Time spent in the queue (histogram). |
| `ggr_host_free_slots` | `host` | Free slots from the last `/status` poll. |
| `ggr_host_breaker_open` | `host` | `1` while the circuit breaker of a host is open. |
| `ggr_orphan_sessions_deleted_total` | `result` | Sessions deleted because the client went away: `ok`, `error`. |

New log statuses: `SESSION_QUEUED`, `BREAKER_OPEN`, `BREAKER_CLOSED`, `ORPHAN_SESSION_DELETED`, `ORPHAN_SESSION_NOT_DELETED`, `HOST_STATUS_UNKNOWN` (verbose only).

## Running

```bash
docker run -d --name ggr -p 4444:4444 \
    -v /etc/grid-router:/etc/grid-router:ro \
    ghcr.io/i-shulgan/ggr:latest -queue-timeout 5m -status-interval 5s
```

Arguments after the image name are appended to the default ones (`-listen :4444 -users /etc/grid-router/users.htpasswd -quotaDir /etc/grid-router/quota`). The container runs as uid 65534, so `users.htpasswd` and quota files must be world-readable. Send `SIGHUP` to reload quota: `docker kill -s HUP ggr`. Quota files, users and the rest of the setup are described in the [upstream guide](http://aerokube.com/ggr/latest/).

Image tags:

| Tag | Source |
|-----|--------|
| `latest` | last commit to `master` |
| `sha-<short-sha>` | a specific commit to `master` |
| `X.Y.Z`, `X.Y`, `X` | release `vX.Y.Z` (see below) |

## Releases and versioning

Versions follow [Semantic Versioning](https://semver.org) and are derived automatically from [Conventional Commits](https://www.conventionalcommits.org) by [release-please](https://github.com/googleapis/release-please), continuing from upstream `1.7.2`:

| Commit | Example | Version bump |
|--------|---------|--------------|
| `fix:` | `fix: delete orphan sessions` | patch, `1.8.0` → `1.8.1` |
| `feat:` | `feat: queue new session requests` | minor, `1.7.2` → `1.8.0` |
| `!` or `BREAKING CHANGE:` footer | `feat!: drop -guests-quota flag` | major, `1.8.0` → `2.0.0` |
| `build:`, `ci:`, `docs:`, `test:`, `refactor:`, `chore:` | `ci: cache Go modules` | none |

How a release happens:

1. Merge pull requests into `master`. With squash merging, the PR title becomes the commit message, so the `pr-title` check requires it to follow Conventional Commits.
2. After each push to `master`, release-please opens or updates a `chore(master): release X.Y.Z` pull request with the version bump and `CHANGELOG.md`.
3. Merge that pull request when you want to release. CI then tags `vX.Y.Z`, publishes a GitHub release with the changelog and pushes `ghcr.io/i-shulgan/ggr:X.Y.Z`, `:X.Y` and `:X`. `ggr -version` and `/ping` report `vX.Y.Z`.

To force a specific version, add a `Release-As: X.Y.Z` footer to a commit. To rewrite changelog entries of a squash-merged PR, put Conventional Commit messages between `BEGIN_COMMIT_OVERRIDE` and `END_COMMIT_OVERRIDE` in its description.

Release-please needs **Settings → Actions → General → Workflow permissions → Allow GitHub Actions to create and approve pull requests** to be enabled. Pull requests created with `GITHUB_TOKEN` do not trigger workflows, so the release PR has no checks; that is expected, as it only changes `CHANGELOG.md` and `.release-please-manifest.json`.

## Building

Image:

```bash
docker build \
    --build-arg GIT_REVISION=$(git describe --tags --always) \
    --build-arg BUILD_STAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
    -t ggr .
```

Add `--build-arg BUILD_TAGS=watch` to reload quota automatically when files change.

Binary (Go 1.27.2 or newer):

```bash
CGO_ENABLED=0 go build -ldflags "-X main.gitRevision=$(git describe --tags --always)"
```

Tests:

```bash
go test -race -tags watch ./...
```

## Articles

* [Selenium testing: a new hope (part I)](https://hackernoon.com/selenium-testing-a-new-hope-7fa87a501ee9)
* [Selenium testing: a new hope (part II)](https://hackernoon.com/selenium-testing-a-new-hope-a00649cdb100)
