#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${DATABASE_URL:-}" ]]; then
  printf 'error: DATABASE_URL is required\n' >&2
  exit 1
fi

if ! command -v psql >/dev/null 2>&1; then
  printf 'error: psql is required to load fixtures\n' >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"

psql "${DATABASE_URL}" \
  --single-transaction \
  -v ON_ERROR_STOP=1 \
  -f "${repo_root}/migrations/001_commerce.sql" \
  -f "${repo_root}/fixtures/commerce.sql"
