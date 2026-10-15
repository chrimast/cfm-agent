#!/bin/sh
set -eu

REPO="chrimast/cfm-agent"
GITHUB_PROXY=""
INSTALL_VERSION="latest"
INSTALL_USER=""
CFM_HOME="/opt/cfm"

log() {
    printf '%s\n' "$*"
}

die() {
    printf '[ERROR] %s\n' "$*" >&2
    exit 1
}

need_value_for=""
for arg in "$@"; do
    if [ -n "$need_value_for" ]; then
        case "$need_value_for" in
            proxy) GITHUB_PROXY="$arg" ;;
            version) INSTALL_VERSION="$arg" ;;
            user) INSTALL_USER="$arg" ;;
        esac
        need_value_for=""
        continue
    fi
    case "$arg" in
        --install-ghproxy=*) GITHUB_PROXY="${arg#*=}" ;;
        --install-ghproxy) need_value_for="proxy" ;;
        --install-version=*) INSTALL_VERSION="${arg#*=}" ;;
        --install-version) need_value_for="version" ;;
        --install-user=*) INSTALL_USER="${arg#*=}" ;;
        --install-user) need_value_for="user" ;;
    esac
done

detect_os() {
    os="$(uname -s 2>/dev/null || printf unknown)"
    case "$os" in
        Linux) printf linux ;;
        Darwin) printf darwin ;;
        FreeBSD) printf freebsd ;;
        MINGW*|MSYS*|CYGWIN*) printf windows ;;
        *) die "unsupported OS: $os" ;;
    esac
}

# MIPS uname -m does NOT reveal byte order: modern kernels report "mips" for both
# big-endian (ath79/ar71xx) and little-endian (ramips MT762x) routers, so mapping
# uname -m directly to an asset hands little-endian devices the big-endian binary,
# which the kernel refuses to exec and BusyBox sh then mis-parses as a script
# ("syntax error: unexpected (""). We read the e_ident[EI_DATA] byte (offset 5;
# 1=LSB/little, 2=MSB/big) of a native ELF to tell them apart. The device's own
# shell is probed, never the freshly downloaded bootstrap binary, which may itself
# be the wrong endianness. Falls back to little-endian (the common OpenWrt case).
detect_mips_endian() {
    probe=""
    for cand in /bin/sh /bin/busybox "$(command -v sh 2>/dev/null || true)" "$(command -v od 2>/dev/null || true)" /usr/bin/env; do
        if [ -n "$cand" ] && [ -f "$cand" ]; then
            probe="$cand"
            break
        fi
    done
    if [ -z "$probe" ]; then
        printf le
        return
    fi
    case "$(od -An -tu1 -j5 -N1 "$probe" 2>/dev/null | tr -d ' \t\n')" in
        2) printf be ;;
        1) printf le ;;
        *) printf le ;;
    esac
}

detect_arch() {
    arch="$(uname -m 2>/dev/null || printf unknown)"
    case "$arch" in
        x86_64|amd64) printf amd64 ;;
        aarch64|arm64) printf arm64 ;;
        i386|i686) printf 386 ;;
        armv5*) if [ "${os_name:-}" = "freebsd" ]; then printf arm; else printf armv5; fi ;;
        armv6*) if [ "${os_name:-}" = "freebsd" ]; then printf arm; else printf armv6; fi ;;
        armv7*|armv8l) if [ "${os_name:-}" = "freebsd" ]; then printf arm; else printf armv7; fi ;;
        # MIPS assets are built with GOMIPS=softfloat so they also run on router
        # CPUs without an FPU; the asset name matches GOARCH, which is what the
        # in-agent auto updater looks for. Endianness comes from detect_mips_endian,
        # not from uname -m, because uname -m cannot distinguish it.
        mips|mipsel|mipsle|lmips)
            if [ "$(detect_mips_endian)" = be ]; then printf mips; else printf mipsle; fi ;;
        mips64|mips64el|mips64le|lmips64)
            if [ "$(detect_mips_endian)" = be ]; then printf mips64; else printf mips64le; fi ;;
        loongarch64|loong64) printf loong64 ;;
        *) die "unsupported architecture: $arch" ;;
    esac
}

as_root() {
    if [ "$(id -u)" -eq 0 ]; then
        "$@"
    elif command -v sudo >/dev/null 2>&1; then
        sudo "$@"
    else
        die "need root or sudo for dedicated $INSTALL_USER user"
    fi
}

prepare_install_user() {
    [ -n "$INSTALL_USER" ] || return 0
    [ "$(uname -s 2>/dev/null || printf unknown)" = Linux ] || die "dedicated $INSTALL_USER user requires systemd Linux"
    if [ ! -d /run/systemd/system ] || ! command -v systemctl >/dev/null 2>&1 || ! command -v loginctl >/dev/null 2>&1 || ! command -v useradd >/dev/null 2>&1 || ! command -v runuser >/dev/null 2>&1; then
        die "dedicated $INSTALL_USER user requires systemd Linux. Use OpenWrt/Alpine/Synology/FreeBSD for those systems."
    fi
    as_root mkdir -p "$CFM_HOME"
    id "$INSTALL_USER" >/dev/null 2>&1 || as_root useradd -m -d "$CFM_HOME" -s /bin/sh "$INSTALL_USER"
    as_root usermod -d "$CFM_HOME" "$INSTALL_USER" >/dev/null 2>&1 || true
    as_root chown "$INSTALL_USER:$INSTALL_USER" "$CFM_HOME"
    as_root loginctl enable-linger "$INSTALL_USER"
    CFM_UID="$(id -u "$INSTALL_USER")"
    as_root systemctl start "user@${CFM_UID}.service" >/dev/null 2>&1 || true
    log "  user    : $INSTALL_USER ($CFM_HOME)"
}

prepare_uninstall_user() {
    [ -n "$INSTALL_USER" ] || return 0
    command -v runuser >/dev/null 2>&1 || die "dedicated $INSTALL_USER uninstall requires runuser"
    id "$INSTALL_USER" >/dev/null 2>&1 || die "user $INSTALL_USER does not exist; this host was not installed as the dedicated user"
    CFM_UID="$(id -u "$INSTALL_USER")"
}

run_as_install_user() {
    if [ -z "$INSTALL_USER" ] || [ "$(id -un 2>/dev/null || true)" = "$INSTALL_USER" ]; then
        "$@"
        return
    fi
    as_root runuser -u "$INSTALL_USER" -- env HOME="$CFM_HOME" XDG_RUNTIME_DIR="/run/user/${CFM_UID}" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/${CFM_UID}/bus" "$@"
}

download() {
    url="$1"
    out="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fL --connect-timeout 10 -m 120 -o "$out" "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -O "$out" "$url"
    else
        die "curl or wget is required for bootstrap download"
    fi
}

run_payload() {
    bin="$1"
    shift
    skip_user_arg=""
    first_arg=1
    for arg in "$@"; do
        if [ -n "$skip_user_arg" ]; then
            skip_user_arg=""
            continue
        fi
        case "$arg" in
            --install-user=*) continue ;;
            --install-user) skip_user_arg=1; continue ;;
        esac
        if [ "$first_arg" = 1 ]; then
            set -- "$arg"
            first_arg=
        else
            set -- "$@" "$arg"
        fi
    done
    if [ "$first_arg" = 1 ]; then
        set --
    fi
    case "${cmd:-${1:-install}}" in
        uninstall|remove|delete|purge)
            run_as_install_user "$bin" "$cmd"
            return
            ;;
    esac
    if [ "$#" -eq 0 ]; then
        run_as_install_user "$bin" install
        return
    fi
    run_as_install_user "$bin" "$@"
}

run_payload_checked() {
    err_file="$1"
    shift
    if run_payload "$@" 2>"$err_file"; then
        return 0
    else
        rc=$?
        return "$rc"
    fi
}

dir_has_noexec() {
    dir="$1"
    if command -v findmnt >/dev/null 2>&1; then
        opts="$(findmnt -no OPTIONS -T "$dir" 2>/dev/null || true)"
        case ",$opts," in
            *,noexec,*) return 0 ;;
        esac
    fi
    return 1
}

stage_and_run_payload() {
    dir="$1"
    shift
    [ -n "$dir" ] || return 125
    if dir_has_noexec "$dir"; then
        return 126
    fi
    mkdir -p "$dir" 2>/dev/null || return 125
    stage="$dir/.cf-probe-bootstrap.$$"
    stage_err="$stage.err"
    cp "$tmp" "$stage" 2>/dev/null || return 125
    chmod +x "$stage" 2>/dev/null || {
        rm -f "$stage"
        return 125
    }
    if run_payload_checked "$stage_err" "$stage" "$@"; then
        rc=0
    else
        rc=$?
        if [ "$rc" -ne 126 ]; then
            cat "$stage_err" >&2 2>/dev/null || true
        fi
    fi
    rm -f "$stage" "$stage_err"
    return "$rc"
}

cmd="${1:-install}"
case "$cmd" in
    uninstall|remove|delete|purge)
        log "[INFO] downloading temporary uninstaller"
        prepare_uninstall_user
        ;;
    *)
        prepare_install_user
        ;;
esac

os_name="$(detect_os)"
arch_name="$(detect_arch)"
asset="cf-probe-${os_name}-${arch_name}"
if [ "$os_name" = "windows" ]; then
    asset="${asset}.exe"
fi

if [ "$INSTALL_VERSION" = "latest" ]; then
    path="latest/download"
else
    path="download/$INSTALL_VERSION"
fi
url="https://github.com/$REPO/releases/$path/$asset"
if [ -n "$GITHUB_PROXY" ]; then
    url="${GITHUB_PROXY%/}/$url"
fi

tmp_dir="${TMPDIR:-/tmp}"
tmp="${tmp_dir%/}/cf-probe-bootstrap.$$"
tmp_err="$tmp.err"
trap 'rm -f "$tmp" "$tmp_err"' EXIT INT TERM

log "CF-Monitor Go Probe bootstrap"
log "  repo    : $REPO"
log "  version : $INSTALL_VERSION"
log "  target  : $os_name/$arch_name"
log "  asset   : $asset"
log "  url     : $url"

download "$url" "$tmp"
chmod +x "$tmp"

status=126
if ! dir_has_noexec "$tmp_dir"; then
    if run_payload_checked "$tmp_err" "$tmp" "$@"; then
        exit 0
    else
        status=$?
    fi
    if [ "$status" -ne 126 ]; then
        cat "$tmp_err" >&2 2>/dev/null || true
        exit "$status"
    fi
fi

log "[WARN] cannot execute bootstrap binary from $tmp; trying executable staging directories"
if [ -n "${HOME:-}" ]; then
    if stage_and_run_payload "$HOME/.cf-probe/tmp" "$@"; then
        exit 0
    fi
    status=$?
    if [ "$status" -ne 125 ] && [ "$status" -ne 126 ]; then
        exit "$status"
    fi
fi
if [ "$os_name" = "darwin" ]; then
    fallback_dirs="."
else
    fallback_dirs="/usr/local/bin /usr/bin /root ."
fi
for dir in $fallback_dirs; do
    if stage_and_run_payload "$dir" "$@"; then
        exit 0
    fi
    status=$?
    if [ "$status" -ne 125 ] && [ "$status" -ne 126 ]; then
        exit "$status"
    fi
done

die "downloaded binary could not be executed. /tmp may be mounted noexec."
