#!/usr/bin/env bash
# Build linux/amd64 binary and package it as .deb via nFPM.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-0.1.0}"
ARCH="${ARCH:-amd64}"
export VERSION ARCH

mkdir -p bin dist

echo "==> building linux/${ARCH} binary"
GOOS=linux GOARCH="${ARCH}" CGO_ENABLED=0 go build \
  -ldflags "-s -w -X main.Version=${VERSION}+registry-login" \
  -o bin/apinfra ./cmd

find_nfpm() {
  if command -v nfpm >/dev/null 2>&1; then
    command -v nfpm
    return
  fi
  local gobin gopath
  gobin="$(go env GOBIN 2>/dev/null || true)"
  gopath="$(go env GOPATH 2>/dev/null || true)"
  if [[ -n "${gobin}" && -x "${gobin}/nfpm" ]]; then
    echo "${gobin}/nfpm"
    return
  fi
  if [[ -n "${gopath}" && -x "${gopath}/bin/nfpm" ]]; then
    echo "${gopath}/bin/nfpm"
    return
  fi
  if [[ -x "${HOME}/go/bin/nfpm" ]]; then
    echo "${HOME}/go/bin/nfpm"
    return
  fi
  return 1
}

if ! NFPM_BIN="$(find_nfpm)"; then
  echo "nfpm not found. Install one of:"
  echo "  go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest"
  echo "  brew install goreleaser/tap/nfpm"
  echo "Then ensure \$(go env GOPATH)/bin is on your PATH, e.g.:"
  echo "  export PATH=\"\$(go env GOPATH)/bin:\$PATH\""
  exit 1
fi

echo "==> packaging .deb (version=${VERSION} arch=${ARCH}) with ${NFPM_BIN}"
"${NFPM_BIN}" package -f nfpm.yaml -p deb -t dist/

echo "==> done:"
ls -la dist/*.deb
