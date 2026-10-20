#!/usr/bin/env bash
# Regenerates the attribution files the About page and release archives use:
#   internal/attribution/go_modules.csv  the Go modules compiled into Gofer
#   internal/attribution/notices.txt     every third-party license text
# Run it after changing dependencies. With --check it only reports whether the
# committed files are out of date, and fails if they are.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GO="${GO:-go}"
GO_LICENSES="github.com/google/go-licenses/v2@v2.0.1"
SELF="github.com/cristianadrielbraun/gofer"
OUT="internal/attribution"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

printf '{{ range . }}{{ .Name }},{{ .Version }},{{ .LicenseName }},{{ .LicenseURL }}\n{{ end }}' > "$work/report.tpl"
"$GO" run "$GO_LICENSES" report . --ignore "$SELF" --template "$work/report.tpl" 2>/dev/null |
  LC_ALL=C sort -u > "$work/go_modules.csv"
"$GO" run "$GO_LICENSES" save . --ignore "$SELF" --save_path "$work/saved" 2>/dev/null

if grep -q ',Unknown,' "$work/go_modules.csv"; then
  echo "go-licenses could not identify these licenses; check them by hand:" >&2
  grep ',Unknown,' "$work/go_modules.csv" >&2
  exit 1
fi

rule="================================================================================"
section() { # name version license url
  printf '\n%s\n%s%s\nLicense: %s\n%s\n\n' "$rule" "$1" "${2:+ $2}" "$3" "$4"
}

{
  echo "Third-party software in Gofer"
  echo
  echo "Gofer includes the software below. Each entry gives its license and the"
  echo "full license text. Gofer's own license is in the LICENSE file."

  tail -n +2 "$OUT/bundled.csv" | while IFS=, read -r name version license url text; do
    section "$name" "$version" "$license" "$url"
    cat "$text"
  done

  prev=""
  while IFS=, read -r name version license url; do
    # A module with several licenses has one report line for each.
    [[ "$name" == "$prev" ]] && continue
    prev="$name"
    licenses="$(awk -F, -v n="$name" '$1 == n { print $3 }' "$work/go_modules.csv" | paste -sd, - | sed 's/,/, /g')"
    section "$name" "$version" "$licenses" "https://pkg.go.dev/$name"
    for f in $(find "$work/saved/$name" -maxdepth 1 -type f | LC_ALL=C sort); do
      cat "$f"
      echo
    done
  done < "$work/go_modules.csv"
} > "$work/notices.txt"

if [[ "${1:-}" == "--check" ]]; then
  if cmp -s "$work/go_modules.csv" "$OUT/go_modules.csv" && cmp -s "$work/notices.txt" "$OUT/notices.txt"; then
    echo "Attribution files are up to date."
    exit 0
  fi
  echo "Attribution files are out of date; run: task licenses" >&2
  exit 1
fi

cp "$work/go_modules.csv" "$work/notices.txt" "$OUT/"
echo "Wrote $OUT/go_modules.csv and $OUT/notices.txt."
