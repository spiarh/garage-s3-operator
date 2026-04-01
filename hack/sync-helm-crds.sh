#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_dir="${repo_root}/deploy/kustomize/crd/bases"
destination_dir="${repo_root}/deploy/helm/garage-s3-operator-crds/templates"

mkdir -p "${destination_dir}"
rm -f "${destination_dir}"/*.yaml

shopt -s nullglob
crd_files=("${source_dir}"/*.yaml)
shopt -u nullglob

if [[ ${#crd_files[@]} -eq 0 ]]; then
  echo "No CRD manifests found in ${source_dir}" >&2
  exit 1
fi

for source_file in "${crd_files[@]}"; do
  destination_file="${destination_dir}/$(basename "${source_file}")"
  cat "${source_file}" >> "${destination_file}"
  echo "Synced $(basename "${source_file}")"
done
