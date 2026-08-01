#!/bin/sh
# Build the CPA plugin as a c-shared dynamic library for the current platform.
# Usage: ./build.sh [output-dir]
set -eu

cd "$(dirname "$0")"
export CGO_ENABLED=1

OUT_DIR="${1:-dist}"
mkdir -p "$OUT_DIR"

UNAME_S=$(uname -s)
case "$UNAME_S" in
  Darwin) EXT="dylib" ;;
  Linux)  EXT="so" ;;
  *)      EXT="dll" ;;
esac

echo "building cpa.$EXT -> $OUT_DIR"
go build -buildmode=c-shared -o "$OUT_DIR/cpa.$EXT" .
rm -f "$OUT_DIR/cpa.h"

echo "done: $OUT_DIR/cpa.$EXT"
