#!/usr/bin/env bash
# Regenerates go/service/filingtypes_gen.go from SEC EDGAR's free full-index.
#
# EDGAR publishes a form.idx per quarter whose first column is the form
# type of every filing made that quarter. Unioning that column across a
# wide span of quarters yields the form-type universe without inventing a
# single entry. Retired forms (10-K405, 10KSB) only appear in old quarters,
# which is why the sweep reaches back to 1995.
#
# SEC serves these files only to a User-Agent carrying a real contact
# email, so set SEC_USER_AGENT before running:
#   SEC_USER_AGENT="Your Project you@example.com" tools/genformtypes.sh
set -euo pipefail

: "${SEC_USER_AGENT:?set SEC_USER_AGENT to \"Your Project you@example.com\"; SEC 403s without a contact email}"
OUT="$(cd "$(dirname "$0")/.." && pwd)/go/service/filingtypes_gen.go"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

QUARTERS=""
for y in $(seq 1995 2025); do QUARTERS="$QUARTERS $y/QTR1 $y/QTR3"; done

for q in $QUARTERS; do
  curl -sf -H "User-Agent: $SEC_USER_AGENT" \
    "https://www.sec.gov/Archives/edgar/full-index/$q/form.idx" \
  | awk 'NR>10 {t=substr($0,1,12); gsub(/^ +| +$/,"",t); if (t!="" && t!="Form Type") print t}' \
    >> "$WORK/types.txt" || echo "skipped $q" >&2
  sleep 0.4
done

sort -u "$WORK/types.txt" | grep -v '^-*$' > "$WORK/final.txt"
echo "collected $(wc -l < "$WORK/final.txt") form types" >&2
echo "now regenerate $OUT from $WORK/final.txt (see the file's own header for the format)" >&2
