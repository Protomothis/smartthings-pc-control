#!/usr/bin/env bash
#
# Create the six custom capabilities and their presentations on the
# SmartThings account.
#
#   cd edge && ./tools/create-capabilities.sh
#
# Run this ONCE per account. `capabilities:create` has no "create or update"
# mode: running it again makes a second set of capabilities under the same
# namespace with the same names, and there is no CLI command to delete a
# capability that a driver has ever referenced. If a definition needs to change
# afterwards, use `smartthings capabilities:update <id> <version> -i <file>`.
#
# Prerequisites:
#   - `smartthings` CLI installed (npm i -g @smartthings/cli)
#   - logged in (the CLI opens a browser on the first command that needs auth;
#     CLI 2.x has no `login` command), or SMARTTHINGS_TOKEN exported
#
# The namespace SmartThings assigns is printed at the end; feed it to
# tools/apply-namespace.js to put it into caps.lua, the profiles and the
# capability JSON.
set -euo pipefail

cd "$(dirname "$0")/.."

# Order matters only for readability; the capabilities are independent.
CAPABILITIES=(pcPower pcExec pcDefer pcUser pcInfo pcVersion)

if ! command -v smartthings >/dev/null 2>&1; then
  echo "error: the 'smartthings' CLI is not on PATH." >&2
  echo "       npm i -g @smartthings/cli   (the first command opens a browser to log in)" >&2
  exit 1
fi

for name in "${CAPABILITIES[@]}"; do
  for file in "capabilities/${name}.json" "capabilities/${name}.presentation.json"; do
    if [ ! -f "$file" ]; then
      echo "error: missing $file (run this from the edge/ directory)" >&2
      exit 1
    fi
  done
done

# Pulls a top-level string field out of the CLI's --json output without
# requiring jq, which is not a given on a Windows box running Git Bash.
json_field() {
  local key="$1" json="$2"
  printf '%s' "$json" |
    tr -d '\n' |
    grep -oE "\"${key}\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
    head -n 1 |
    sed -E "s/.*:[[:space:]]*\"([^\"]*)\"/\1/"
}

namespace=""

for name in "${CAPABILITIES[@]}"; do
  echo "==> creating capability ${name}"
  created=$(smartthings capabilities:create -i "capabilities/${name}.json" --json)
  printf '%s\n' "$created"

  id=$(json_field id "$created")
  version=$(json_field version "$created")
  # `version` comes back as a number in the JSON, so json_field misses it;
  # a freshly created capability is always version 1.
  [ -n "$version" ] || version=1

  if [ -z "$id" ]; then
    echo "error: could not read the capability id out of the CLI output above." >&2
    echo "       Create the rest by hand, then run tools/apply-namespace.js." >&2
    exit 1
  fi

  if [ -z "$namespace" ]; then
    # Capability ids are "<namespace>.<name>".
    namespace="${id%%.*}"
    echo "--> SmartThings assigned the namespace: ${namespace}"
  fi

  echo "==> creating presentation for ${id} (version ${version})"
  smartthings capabilities:presentation:create "$id" "$version" -i "capabilities/${name}.presentation.json"
  echo
done

echo "All ${#CAPABILITIES[@]} capabilities and presentations created."
echo
echo "Namespace: ${namespace}"
echo
echo "Now write it into the driver and re-run the tests:"
echo
echo "    node tools/apply-namespace.js ${namespace}"
echo "    npm test"
echo
echo "Then package the driver (or push an edge-vX.Y.Z tag and let CI do it):"
echo
echo "    smartthings edge:drivers:package ."
