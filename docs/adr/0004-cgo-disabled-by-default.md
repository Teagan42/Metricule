# ADR 0004: CGO disabled by default

Status: Accepted
Date: 2026-10-21

## Context

§4.3 states there must be no CGO requirement in the default release unless a
demonstrated need justifies it. §16.1 wants a small multi-architecture image, and
§1.1 wants the binary small enough to run as a sidecar.

The only dependency that would normally force cgo is the SQLite driver.

## Decision

Set `CGO_ENABLED=0` in the Makefile, the Dockerfile, and CI. The SQLite adapter uses
`modernc.org/sqlite`, which is pure Go.

The race-detector CI job overrides this, because `-race` requires cgo.

## Consequences

The binary is statically linked, so the runtime image is
`gcr.io/distroless/static-debian12:nonroot` rather than a glibc base. That removes
the libc attack surface and makes `linux/arm64` cross-compilation a plain
`GOOS`/`GOARCH` build with no cross-toolchain.

Go's cgo-based DNS resolver is unavailable, so the pure-Go resolver is used. For a
sidecar resolving a handful of service names this is not a limitation, but it does
mean `/etc/nsswitch.conf` and some exotic NSS configurations are ignored.

Practical note for contributors: `make test-race` enables cgo and therefore depends on
a working local C toolchain and platform SDK. A broken SDK will fail to link the race
binary while every non-race target still builds, which can look like a project
problem and is not. CI runs the race detector on Linux, which is authoritative.
