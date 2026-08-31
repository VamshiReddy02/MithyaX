#!/usr/bin/env bash
# package.sh — builds mithyax-extension.zip, the file a pilot tester
# downloads and unzips per INSTALL.md. Run from anywhere; always
# operates on this script's own directory.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

OUT="mithyax-extension.zip"
rm -f "$OUT"

# Only what a running extension actually needs — excludes this
# repo's own dev/doc/build files so nothing internal ends up in a
# tester's unzipped folder.
zip -r "$OUT" . \
  -x "package.sh" \
  -x "$OUT" \
  -x "README.md" \
  -x "INSTALL.md" \
  -x ".DS_Store" \
  -x "*/.DS_Store"

echo "Wrote $(pwd)/$OUT"
