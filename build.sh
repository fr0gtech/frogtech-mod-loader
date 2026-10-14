#!/usr/bin/env bash
# Builds dist/FrogLoader.exe, dist/FrogLoader_v<version>.zip,
# dist/THIRD_PARTY_NOTICES.txt and dist/SHA256SUMS.
# Usage: ./build.sh [version]   (default: the VERSION file)
#
# The same script runs in GitHub Actions (.github/workflows/release.yml). The
# build is reproducible: the same tag gives byte-identical files anywhere
# (-trimpath, no VCS stamp, empty build id, fixed zip timestamps, pinned Go).
#
# Antivirus hygiene: version info, manifest and icon are embedded; the exe is
# not stripped (-s -w) and not packed (no UPX).
set -euo pipefail
cd "$(dirname "$0")"
GO="${GO:-go}"

VERSION="${1:-$(cat VERSION)}"
export GOTOOLCHAIN=local CGO_ENABLED=0
GOVER="$("$GO" env GOVERSION)"
[ "$GOVER" = "go$(cat .go-version)" ] || echo "warning: building with $GOVER, releases use go$(cat .go-version); the sha256 will differ" >&2

"$GO" vet ./...
GOOS=windows GOARCH=amd64 "$GO" vet ./...
"$GO" test ./...

# Windows resources (icon, manifest, version info): a .syso in the main
# package's folder is linked automatically.
"$GO" run ./tools/winres -version "$VERSION" -o cmd/frogloader/rsrc_windows_amd64.syso

mkdir -p dist
rm -f dist/FrogLoader.exe dist/FrogLoader_v*.zip dist/SHA256SUMS dist/THIRD_PARTY_NOTICES.txt
GOOS=windows GOARCH=amd64 "$GO" build -trimpath -buildvcs=false \
  -ldflags "-H windowsgui -buildid= -X main.version=$VERSION" -o dist/FrogLoader.exe ./cmd/frogloader

# Licenses of everything compiled into the exe (MIT/BSD/Apache/ISC/MPL need
# their texts shipped with the binary).
{
  echo "Frogtech Mod Loader includes the following third-party software."
  echo "Font: Jost (SIL Open Font License 1.1), see internal/server/web/fonts/OFL-Jost.txt in the source."
  GOOS=windows GOARCH=amd64 "$GO" list -deps -f '{{with .Module}}{{.Path}} {{.Version}} {{.Dir}}{{end}}' ./cmd/frogloader | sort -u |
  while read -r path ver dir; do
    [ "$path" = "github.com/fr0gtech/frogtech-mod-loader" ] && continue
    lic="$(ls "$dir" | grep -iE '^(licen[cs]e|copying)' | head -1)"
    printf '\n==============================================================================\n%s %s\n==============================================================================\n\n' "$path" "$ver"
    cat "$dir/$lic"
  done
} > dist/THIRD_PARTY_NOTICES.txt

# Release/self-update package: FrogLoader.exe (+ the notices) at the root,
# written by tools/pack (fixed timestamps, Go's deflate: reproducible).
ZIP="dist/FrogLoader_v$VERSION.zip"
"$GO" run ./tools/pack -o "$ZIP" dist/FrogLoader.exe dist/THIRD_PARTY_NOTICES.txt

cmp -s dist/THIRD_PARTY_NOTICES.txt THIRD_PARTY_NOTICES.txt || echo "warning: THIRD_PARTY_NOTICES.txt is out of date; copy it from dist/" >&2

(cd dist && sha256sum FrogLoader.exe "FrogLoader_v$VERSION.zip" THIRD_PARTY_NOTICES.txt > SHA256SUMS)
ls -l dist
cat dist/SHA256SUMS
