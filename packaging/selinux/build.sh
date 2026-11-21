#!/usr/bin/env bash
# Builds the iormesh-selinux RPM (iormesh.te, iormesh-selinux.spec; task
# 7h4) on Rocky Linux 9. `mage selinuxRPM` runs it in a throwaway Rocky 9
# container with this directory at /src (read-only) and the output
# directory at /out; on an el9 build host it also runs directly:
#
#   build.sh SRC_DIR OUT_DIR
#
# It installs the build requirements (selinux-policy-devel,
# container-selinux, rpm-build, make, bzip2) when they are missing, so
# only a throwaway container or a build host should run it. The binary
# RPM lands in OUT_DIR, the source RPM beside it; with IORMESH_OUT_OWNER
# (uid:gid, set under Docker, which runs as real root) both are handed to
# that owner.
set -euo pipefail

declare -r SRC="${1:-/src}"
declare -r OUT="${2:-/out}"
declare -r PKGS=(selinux-policy-devel container-selinux rpm-build make bzip2)

_install() {
    local missing=() p
    for p in "${PKGS[@]}"; do
        rpm -q "$p" > /dev/null 2>&1 || missing+=("$p")
    done
    (( ${#missing[@]} == 0 )) && return 0
    if command -v dnf > /dev/null; then
        dnf -y -q install "${missing[@]}"
    else
        microdnf -y install "${missing[@]}"
    fi
}

main() {
    _install
    local top
    top="$(mktemp -d)"
    mkdir -p "$top/SOURCES" "$OUT"
    cp "$SRC/iormesh.te" "$top/SOURCES/"
    rpmbuild --define "_topdir $top" -ba "$SRC/iormesh-selinux.spec"
    cp "$top"/RPMS/noarch/iormesh-selinux-*.rpm "$top"/SRPMS/*.src.rpm "$OUT/"
    rm -rf "$top"
    if [[ -n "${IORMESH_OUT_OWNER:-}" ]]; then
        chown "$IORMESH_OUT_OWNER" "$OUT"/iormesh-selinux-*.rpm
    fi
    ls -l "$OUT"
}

main "$@"
