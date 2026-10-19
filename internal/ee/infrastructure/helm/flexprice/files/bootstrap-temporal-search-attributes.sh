#!/bin/sh
# Explicit provisioning ONLY. Never called by worker startup or incident recovery.
set -eu
: "${TEMPORAL_ADDRESS:?set the frontend address}"
: "${TEMPORAL_NAMESPACE:?set an explicit namespace}"

list_attributes() {
  temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" \
    operator search-attribute list --output json
}

is_keyword() {
  printf '%s\n' "$attributes" | grep -Eq '"'"$name"'"[[:space:]]*:[[:space:]]*"(Keyword|INDEXED_VALUE_TYPE_KEYWORD)"'
}

for name in SubscriptionID TenantID EnvironmentID FailingActivity FailureReason; do
  attributes=$(list_attributes)
  if is_keyword; then
    continue
  fi
  if printf '%s\n' "$attributes" | grep -Eq '"'"$name"'"[[:space:]]*:'; then
    echo "ERROR: $name exists with an incompatible type; refusing to retype" >&2
    exit 1
  fi
  if ! temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" \
    operator search-attribute create --name "$name" --type Keyword; then
    # A competing bootstrap may have created it. Re-read and verify rather
    # than swallowing denied writes, exhausted slots or connectivity failures.
    attributes=$(list_attributes)
    if ! is_keyword; then
      echo "ERROR: could not provision $name as Keyword" >&2
      exit 1
    fi
  fi
done
# Verify the final contract, including after successful create responses.
attributes=$(list_attributes)
for name in SubscriptionID TenantID EnvironmentID FailingActivity FailureReason; do
  if ! is_keyword; then
    echo "ERROR: $name is not registered as Keyword" >&2
    exit 1
  fi
done
