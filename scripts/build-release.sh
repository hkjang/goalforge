#!/usr/bin/env bash
# Builds the runnable release artifacts: one archive per platform, each holding
# a single static binary plus the guide, and a SHA256SUMS file covering them.
#
# The binaries are pure Go (CGO_ENABLED=0) because the SQLite driver is, so
# cross-compiling needs no toolchain per target and the result runs on a machine
# with no libc of a particular vintage. -trimpath keeps build paths out of the
# binary so two builds of the same commit are the same bytes.
set -euo pipefail

version="${1:-}"
if [[ -z "$version" ]]; then
  echo "usage: scripts/build-release.sh vX.Y.Z" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/dist/$version"
commit="$(git -C "$root" rev-parse HEAD)"
# Reproducible: the commit's own date, not the moment the script happened to run.
built="$(git -C "$root" show -s --format=%cI "$commit")"

rm -rf "$out"
mkdir -p "$out"

# Windows users expect a .zip. Rather than require the zip binary on whatever
# machine cuts the release, fall back to python's zipfile, which is present
# wherever either one is.
zip_dir() {
  local stage="$1" target="$2"
  if command -v zip >/dev/null 2>&1; then
    (cd "$stage" && zip -q -r "$target" .)
    return
  fi
  python3 - "$stage" "$target" <<'PYZIP'
import os, sys, zipfile
stage, target = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as archive:
    for root, _, files in os.walk(stage):
        for name in sorted(files):
            path = os.path.join(root, name)
            info = zipfile.ZipInfo(os.path.relpath(path, stage))
            # The executable bit has to survive the archive, or the file a
            # Windows user extracts is not runnable on WSL or Git Bash.
            info.external_attr = (os.stat(path).st_mode & 0xFFFF) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            with open(path, "rb") as handle:
                archive.writestr(info, handle.read())
PYZIP
}

platforms=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

ldflags="-s -w"
ldflags+=" -X main.version=$version"
ldflags+=" -X main.commit=$commit"
ldflags+=" -X main.buildDate=$built"

for platform in "${platforms[@]}"; do
  os="${platform%/*}"
  arch="${platform#*/}"
  name="goalforge"
  [[ "$os" == "windows" ]] && name="goalforge.exe"
  stage="$out/stage"
  rm -rf "$stage"
  mkdir -p "$stage"
  echo "building $os/$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$ldflags" -o "$stage/$name" "$root/cmd/goalforge"
  cp "$root/README.md" "$stage/README.md"
  cp "$root/docs/GUIDE.md" "$stage/GUIDE.md"
  archive="goalforge_${version}_${os}_${arch}"
  if [[ "$os" == "windows" ]]; then
    zip_dir "$stage" "$out/$archive.zip"
  else
    tar -czf "$out/$archive.tar.gz" -C "$stage" .
  fi
  rm -rf "$stage"
done

# A checksum file is what makes a download verifiable; without it "runnable
# release" means "trust whatever arrived".
(cd "$out" && sha256sum goalforge_*.tar.gz goalforge_*.zip > SHA256SUMS)
echo
echo "artifacts in $out:"
ls -1 "$out"
