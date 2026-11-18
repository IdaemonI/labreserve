#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
# Python 3.10+ and its venv module are only needed for specification validation.
# The pinned validator is isolated from system Python and application modules.
validator_env="${OPENAPI_VENV:-${TMPDIR:-/tmp}/labreserve-openapi-0.9.0}"
if ! "$validator_env/bin/python" -c \
  'from importlib.metadata import version; assert version("openapi-spec-validator") == "0.9.0"' \
  >/dev/null 2>&1; then
  python3 -m venv "$validator_env"
  "$validator_env/bin/python" -m pip install --disable-pip-version-check \
    openapi-spec-validator==0.9.0
fi
exec "$validator_env/bin/python" -m openapi_spec_validator api/openapi.yaml
