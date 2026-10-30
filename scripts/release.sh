#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' \
  "Automated tag, GitHub Release, and local package publishing are disabled." \
  "Use the manually dispatched 'Release GHCR' workflow for versioned images and charts." \
  "Run a dry-run first; configure required reviewers and main-only protection for ghcr-publish." >&2
exit 1
