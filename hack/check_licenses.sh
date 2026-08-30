#!/usr/bin/env bash
set -euo pipefail

go_licenses='github.com/google/go-licenses/v2@v2.0.1'
securejoin_module='github.com/cyphar/filepath-securejoin'
allowed_licenses='Apache-2.0,BSD-2-Clause,BSD-3-Clause,MIT,ISC'

tool_dir="$(mktemp -d)"
trap 'rm -rf "${tool_dir}"' EXIT
GOBIN="${tool_dir}" go install "${go_licenses}"
go_licenses_bin="${tool_dir}/go-licenses"

for arch in amd64 arm64; do
  echo "checking Go dependency licenses for linux/${arch}"
  GOOS=linux GOARCH="${arch}" CGO_ENABLED=0 \
    "${go_licenses_bin}" check \
      --ignore "${securejoin_module}" \
      --allowed_licenses="${allowed_licenses}" \
      ./cmd

  mapfile -t securejoin_packages < <(
    GOOS=linux GOARCH="${arch}" CGO_ENABLED=0 \
      go list -deps -f \
        '{{if and .Module (eq .Module.Path "github.com/cyphar/filepath-securejoin")}}{{.ImportPath}}{{end}}' \
        ./cmd | sed '/^$/d'
  )
  if [[ ${#securejoin_packages[@]} -eq 0 ]]; then
    echo 'filepath-securejoin is selected but no compiled package was found' >&2
    exit 1
  fi

  for package in "${securejoin_packages[@]}"; do
    non_go_files="$(
      GOOS=linux GOARCH="${arch}" CGO_ENABLED=0 \
        go list -f '{{join .CgoFiles " "}} {{join .CFiles " "}} {{join .CXXFiles " "}} {{join .MFiles " "}} {{join .HFiles " "}} {{join .FFiles " "}} {{join .SFiles " "}} {{join .SysoFiles " "}} {{join .EmbedFiles " "}} {{join .SwigFiles " "}} {{join .SwigCXXFiles " "}}' \
        "${package}" | xargs
    )"
    if [[ -n "${non_go_files}" ]]; then
      echo "unreviewed non-Go filepath-securejoin files for ${package}: ${non_go_files}" >&2
      exit 1
    fi

    while IFS= read -r source_file; do
      [[ -n "${source_file}" ]] || continue
      if ! grep --line-regexp --quiet \
        '// SPDX-License-Identifier: BSD-3-Clause' \
        "${source_file}"; then
        echo "non-BSD filepath-securejoin file selected: ${source_file}" >&2
        exit 1
      fi
    done < <(
      GOOS=linux GOARCH="${arch}" CGO_ENABLED=0 \
        go list -f '{{range .GoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}' "${package}"
    )
  done
done

cmp --silent LICENSE charts/image-updater/LICENSE

grep --fixed-strings --quiet \
  'artifacthub.io/license: Apache-2.0' \
  charts/image-updater/Chart.yaml
