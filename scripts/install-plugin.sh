#!/usr/bin/env sh
# Downloads the release binary matching plugin.yaml's version into bin/.
set -eu

repo="guarnz/deadvalues"
version="$(sed -n 's/^version: *//p' plugin.yaml)"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux|darwin) ext="tar.gz" ;;
  mingw*|msys*|cygwin*) os="windows"; ext="zip" ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

asset="deadvalues_${os}_${arch}.${ext}"
url="https://github.com/${repo}/releases/download/v${version}/${asset}"
sums="https://github.com/${repo}/releases/download/v${version}/checksums.txt"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${url}"
curl -fsSL -o "$tmp/$asset" "$url"
curl -fsSL -o "$tmp/checksums.txt" "$sums"

expected="$(grep " ${asset}\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)"
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "checksum mismatch for $asset" >&2
  exit 1
fi

mkdir -p bin
if [ "$ext" = "zip" ]; then
  unzip -o -q "$tmp/$asset" -d "$tmp/out"
else
  mkdir -p "$tmp/out"
  tar -xzf "$tmp/$asset" -C "$tmp/out"
fi
cp "$tmp"/out/deadvalues* bin/
chmod +x bin/deadvalues* 2>/dev/null || true
echo "deadvalues ${version} installed"
