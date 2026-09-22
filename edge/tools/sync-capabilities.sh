#!/usr/bin/env bash
#
# Push the local capability definitions, presentations and translations to the
# SmartThings account (#78).
#
#   cd edge && ./tools/sync-capabilities.sh            # apply
#   cd edge && ./tools/sync-capabilities.sh --dry-run  # print the commands only
#
# Unlike tools/create-capabilities.sh this is idempotent and is meant to be run
# after every change to capabilities/: `capabilities:update` and
# `capabilities:presentation:update` replace the stored document, and
# `capabilities:translations:upsert` creates or replaces one locale.
#
# It only works while the capabilities are `status: proposed` - SmartThings
# freezes a published capability, and a change then needs a new version.
#
# Prerequisites:
#   - `smartthings` CLI installed (npm i -g @smartthings/cli)
#   - authenticated (CLI 2.x has no `login`; the first command opens a browser),
#     or SMARTTHINGS_TOKEN exported
#   - the ids in capabilities/*.json belong to this account (see §14)
set -euo pipefail

cd "$(dirname "$0")/.."

CAPABILITIES=(pcPowerState pcCommand pcSchedule pcStatus pcSession)
VERSION=1
TAGS=(ko en)

DRY_RUN=0
if [ "${1:-}" = "--dry-run" ]; then
  DRY_RUN=1
fi

if [ "$DRY_RUN" -eq 0 ] && ! command -v smartthings >/dev/null 2>&1; then
  echo "error: the 'smartthings' CLI is not on PATH." >&2
  echo "       npm i -g @smartthings/cli" >&2
  exit 1
fi

# Reads the "id" out of a capability definition without needing jq (Git Bash on
# Windows has none). The id is a top-level "<namespace>.<name>" string.
capability_id() {
  tr -d '\n' <"$1" |
    grep -oE '"id"[[:space:]]*:[[:space:]]*"[^"]*"' |
    head -n 1 |
    sed -E 's/.*:[[:space:]]*"([^"]*)"/\1/'
}

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '    smartthings'
    printf ' %s' "$@"
    printf '\n'
  else
    smartthings "$@"
  fi
}

for name in "${CAPABILITIES[@]}"; do
  definition="capabilities/${name}.json"
  presentation="capabilities/${name}.presentation.json"

  for file in "$definition" "$presentation"; do
    if [ ! -f "$file" ]; then
      echo "error: missing $file (run this from the edge/ directory)" >&2
      exit 1
    fi
  done

  id=$(capability_id "$definition")
  if [ -z "$id" ]; then
    echo "error: no \"id\" in $definition" >&2
    exit 1
  fi

  echo "==> ${name} (${id})"

  echo "  definition"
  run capabilities:update "$id" --capability-version "$VERSION" -i "$definition"

  echo "  presentation"
  run capabilities:presentation:update "$id" --capability-version "$VERSION" -i "$presentation"

  for tag in "${TAGS[@]}"; do
    translation="capabilities/translations/${name}.${tag}.json"
    if [ ! -f "$translation" ]; then
      echo "error: missing $translation" >&2
      exit 1
    fi
    echo "  translation ${tag}"
    run capabilities:translations:upsert "$id" --capability-version "$VERSION" -i "$translation"
  done

  echo
done

echo "Done. Check one in the app, or read it back with:"
echo
echo "    smartthings capabilities:presentation <id> --capability-version ${VERSION}"
echo "    smartthings capabilities:translations <id> --capability-version ${VERSION} ko"
echo
echo "The driver itself is unchanged by this; repackage only when src/ or"
echo "profiles/ changed:"
echo
echo "    smartthings edge:drivers:package ."
