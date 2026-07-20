#!/usr/bin/env bash
set -euo pipefail

version="${1:?version is required}"
directory="${2:-dist}"
plugin="cpa-codex-auto-reset"

expected=(
  "${plugin}_${version}_linux_amd64.zip:${plugin}.so"
  "${plugin}_${version}_linux_arm64.zip:${plugin}.so"
  "${plugin}_${version}_darwin_amd64.zip:${plugin}.dylib"
  "${plugin}_${version}_darwin_arm64.zip:${plugin}.dylib"
  "${plugin}_${version}_windows_amd64.zip:${plugin}.dll"
)

actual_count="$(find "${directory}" -maxdepth 1 -type f -name '*.zip' | wc -l | tr -d ' ')"
if [[ "${actual_count}" -ne "${#expected[@]}" ]]; then
  echo "expected ${#expected[@]} ZIP files, found ${actual_count}" >&2
  exit 1
fi

for contract in "${expected[@]}"; do
  zip_name="${contract%%:*}"
  library_name="${contract#*:}"
  zip_path="${directory}/${zip_name}"
  [[ -f "${zip_path}" ]] || { echo "missing ${zip_name}" >&2; exit 1; }
  entry_count="$(unzip -Z1 "${zip_path}" | wc -l | tr -d ' ')"
  entry_name="$(unzip -Z1 "${zip_path}" | sed -n '1p')"
  if [[ "${entry_count}" -ne 1 || "${entry_name}" != "${library_name}" ]]; then
    echo "invalid ZIP layout for ${zip_name}: ${entry_name}" >&2
    exit 1
  fi
done

if [[ -f "${directory}/checksums.txt" ]]; then
  for contract in "${expected[@]}"; do
    zip_name="${contract%%:*}"
    count="$(awk -v file="${zip_name}" '$2 == file {count++} END {print count+0}' "${directory}/checksums.txt")"
    [[ "${count}" -eq 1 ]] || { echo "checksums.txt must contain exactly one entry for ${zip_name}" >&2; exit 1; }
  done
  checksum_lines="$(wc -l < "${directory}/checksums.txt" | tr -d ' ')"
  [[ "${checksum_lines}" -eq "${#expected[@]}" ]] || { echo "checksums.txt contains extra entries" >&2; exit 1; }
  (cd "${directory}" && sha256sum --check checksums.txt)
fi
