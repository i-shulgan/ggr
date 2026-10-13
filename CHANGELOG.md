# Changelog

## [1.8.0](https://github.com/i-shulgan/ggr/compare/v1.7.2...v1.8.0) (2026-10-13)


### Features

* choose hosts by free slots from polled /status (-status-interval) ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* expose Prometheus metrics at /metrics ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* queue new session requests while all hosts are busy (-queue-timeout, -max-queue) ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* tell busy hosts from start errors and add a circuit breaker (-max-start-failures, -breaker-timeout) ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))


### Bug Fixes

* delete sessions created after the client went away ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* do not mix hosts of different versions matched by prefix ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* do not write the session response after the handler returned ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* drop quota of deleted files on reload ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* parse flags in main and buffer signal channels ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* point se:cdp capability to Ggr devtools endpoint ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* send host credentials when proxying to hosts behind basic auth ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* stop /quota from wiping host credentials ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))


### Build System

* **deps:** bump Go to 1.27.2 and update dependencies ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
* multi-stage Dockerfile with static non-root image published to ghcr.io ([0a3d826](https://github.com/i-shulgan/ggr/commit/0a3d826d5ac55c888d9b8d44d90badf2d2f8474a))
