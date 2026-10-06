#!/usr/bin/env bash
# Installs the deadvalues release binary for the GitHub Actions and adds it to
# the PATH. Used by both action.yml and setup/action.yml.
#
# Without a version input, the binary follows the ref the action was used at,
# so pinning the action also pins the binary: @v0.1.0 installs v0.1.0, @v0
# the latest v0.x release, and a commit SHA the latest release.
set -euo pipefail

repo=guarnz/deadvalues

# release_tag prints the tag to download, or nothing for the latest release.
release_tag() {
  local want="${VERSION:-}" ref="${ACTION_REF:-}"
  if [ "$want" = "latest" ]; then
    return
  fi
  if [ -n "$want" ]; then
    echo "$want"
    return
  fi
  if [[ "$ref" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
    echo "$ref"
  elif [[ "$ref" =~ ^v[0-9]+(\.[0-9]+)?$ ]]; then
    local match
    match="$(gh release list -R "$repo" --exclude-drafts --exclude-pre-releases --limit 200 --json tagName --jq '.[].tagName' \
      | grep -E "^${ref//./\\.}\.[0-9]+(\.[0-9]+)?$" | sort -V | tail -n 1 || true)"
    if [ -z "$match" ]; then
      echo "no release matches $ref" >&2
      return 1
    fi
    echo "$match"
  fi
}

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux|darwin) ;;
  *) echo "unsupported OS: $os (the action runs on Linux and macOS runners)" >&2; exit 1 ;;
esac

asset="deadvalues_${os}_${arch}.tar.gz"
dir="$RUNNER_TEMP/deadvalues"
mkdir -p "$dir"

want="$(release_tag)"
tag=()
if [ -n "$want" ]; then tag=("$want"); fi
echo "downloading deadvalues ${want:-latest} (action ref: ${ACTION_REF:-none})"
gh release download ${tag[@]+"${tag[@]}"} -R "$repo" -p "$asset" -p checksums.txt -D "$dir" --clobber
sha=(sha256sum)
if ! command -v sha256sum >/dev/null; then sha=(shasum -a 256); fi
(cd "$dir" && grep " $asset\$" checksums.txt | "${sha[@]}" -c -)
tar -xzf "$dir/$asset" -C "$dir" deadvalues

echo "$dir" >> "$GITHUB_PATH"
installed="$("$dir/deadvalues" version | awk 'NR == 1 { print $2 }')"
echo "deadvalues $installed installed"
{
  echo "version=$installed"
  echo "path=$dir/deadvalues"
} >> "$GITHUB_OUTPUT"
