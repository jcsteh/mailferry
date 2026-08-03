#!/bin/sh
# MailFerry — IMAP Migration & Sync
# Copyright (C) 2026 Andy Saputra
# Author: Andy Saputra <andy@saputra.org>
# SPDX-License-Identifier: AGPL-3.0-or-later
# Reproducible release build (all six targets).
#
# Usage: ./build.sh [output-dir]        (default: ./dist)
#
# Produces mailferry-v<version>-<os>-<arch>[.exe] + SHA256SUMS.
# CGO is disabled (pure Go, static binaries); -trimpath and a cleared
# buildid keep builds reproducible across machines with the same Go
# toolchain. macOS signing/notarisation happens AFTER this step - see
# docs/RELEASING-MACOS.md.
set -e
# The version comes from MailFerry itself, which composes it from the single
# definition in internal/identity (Core + Stage). This script deliberately
# does NOT parse the source or keep a copy: promoting v2.1.0-RC1 to v2.1.0 is
# a one-line change there and nothing here needs touching.
VERSION=$(go run ./cmd/mailferry version --bare)
[ -n "$VERSION" ] || { echo "could not determine version" >&2; exit 1; }
echo "MailFerry v$VERSION"
OUT=${1:-dist}
mkdir -p "$OUT"
# CHANGELOG.md is embedded from the repository root by changelog_embed.go
export CGO_ENABLED=0
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(date +%s)}
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  GOOS=${t%/*}; GOARCH=${t#*/}; EXT=""
  [ "$GOOS" = "windows" ] && EXT=".exe"
  NAME="mailferry-v$VERSION-$GOOS-$GOARCH$EXT"
  echo "building $NAME"
  GOOS=$GOOS GOARCH=$GOARCH go build -trimpath \
    -ldflags="-s -w -buildid=" -o "$OUT/$NAME" ./cmd/mailferry
done
( cd "$OUT" && sha256sum mailferry-v"$VERSION"-* > SHA256SUMS )
echo "done: $OUT"
