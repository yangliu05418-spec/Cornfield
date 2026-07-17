#!/usr/bin/env bash

# Require an existing absolute directory whose configured spelling is already
# canonical. Comparing readlink -e output also rejects a symlink in any parent
# component, not only a symlink at the leaf.
require_canonical_directory() {
  local path="$1"
  local label="${2:-path}"
  local resolved

  case "${path}" in
    /)
      echo "${label} must not be the filesystem root" >&2
      return 2
      ;;
    /*) ;;
    *)
      echo "${label} must be an absolute path" >&2
      return 2
      ;;
  esac
  if ! test -d "${path}"; then
    echo "${label} must be an existing directory" >&2
    return 2
  fi
  resolved="$(readlink -e -- "${path}")" || {
    echo "${label} cannot be resolved" >&2
    return 2
  }
  if test "${resolved}" != "${path}"; then
    echo "${label} must be canonical and must not contain symlinks" >&2
    return 2
  fi
}

# Root-run maintenance must not execute code or Compose configuration from a
# path that another local account can replace. Check every directory component,
# including the target, because a trusted leaf below a writable parent is not a
# trusted execution boundary.
require_root_owned_path() {
  local path="$1"
  local label="${2:-path}"
  local current mode owner

  case "${path}" in
    /*) ;;
    *)
      echo "${label} must be an absolute path" >&2
      return 2
      ;;
  esac
  current="${path}"
  while :; do
    test -d "${current}" || {
      echo "${label} contains a non-directory component: ${current}" >&2
      return 2
    }
    owner="$(stat -c '%u' "${current}")" || return 2
    mode="$(stat -c '%a' "${current}")" || return 2
    if test "${owner}" != "0" || (( (8#${mode} & 0022) != 0 )); then
      echo "${label} must be below root-owned directories that are not group/world writable: ${current}" >&2
      return 2
    fi
    test "${current}" = "/" && break
    current="$(dirname -- "${current}")"
  done
}
