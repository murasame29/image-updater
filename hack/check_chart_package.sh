#!/usr/bin/env bash
set -euo pipefail

archive="${1:?usage: check_chart_package.sh <chart-archive> [chart-directory]}"
chart_dir="${2:-charts/image-updater}"
chart_root='image-updater'

test -f "${archive}"

archive_entries="$(tar --list --file "${archive}")"
for notice in LICENSE NOTICE; do
  archive_path="${chart_root}/${notice}"
  if ! grep --line-regexp --fixed-strings --quiet \
    "${archive_path}" <<< "${archive_entries}"; then
    echo "chart archive is missing ${archive_path}" >&2
    exit 1
  fi

  if ! tar --extract --to-stdout --file "${archive}" "${archive_path}" \
    | cmp --silent "${chart_dir}/${notice}" -; then
    echo "chart archive ${archive_path} does not match ${chart_dir}/${notice}" >&2
    exit 1
  fi
done
