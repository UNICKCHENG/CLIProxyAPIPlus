#!/usr/bin/env bash
set -euo pipefail

models_repository="${MODELS_REPOSITORY_URL:-https://github.com/router-for-me/models.git}"
models_token="${MODELS_REPOSITORY_TOKEN:-}"
models_ref="${MODELS_REPOSITORY_REF:-main}"
catalog_dir="${MODEL_CATALOG_DIR:-internal/registry/models}"
codex_catalog="$catalog_dir/codex_client_models.json"
codex_candidate="$(mktemp)"
trap 'rm -f "$codex_candidate"' EXIT

# Inject credentials for private GitHub repositories when a token is provided.
if [[ -n "$models_token" && "$models_repository" == https://github.com/* ]]; then
	models_repository="https://x-access-token:${models_token}@github.com/${models_repository#https://github.com/}"
fi

# Fetch failures must not break the build: embedded catalogs remain the fallback.
fetch_ok=false
if git fetch --depth 1 "$models_repository" "$models_ref"; then
	fetch_ok=true
else
	printf '::warning::models repository fetch failed; keeping embedded catalogs.\n'
fi

if [[ "$fetch_ok" == true ]]; then
	if ! git show FETCH_HEAD:models.json > "$catalog_dir/models.json"; then
		printf '::warning::Remote models.json is missing; using embedded fallback.\n'
	fi

	if git show FETCH_HEAD:codex_client_models.json > "$codex_candidate" &&
		go run ./cmd/validate_codex_models --file "$codex_candidate"; then
		mv "$codex_candidate" "$codex_catalog"
		printf 'Refreshed validated Codex client model catalog.\n'
	else
		printf '::warning::Remote Codex client model catalog is missing or invalid; using embedded fallback.\n'
	fi
fi

go run ./cmd/validate_codex_models --file "$codex_catalog"
