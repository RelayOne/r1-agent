#!/usr/bin/env bash
set -euo pipefail

# Stoke installer
# Usage: curl -fsSL https://raw.githubusercontent.com/ericmacdougall/Stoke/main/install.sh | bash

REPO="ericmacdougall/Stoke"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY="stoke"

info() { echo "==> $*"; }
error() { echo "ERROR: $*" >&2; exit 1; }

detect_platform() {
    local os arch

    case "$(uname -s)" in
        Linux)  os="linux" ;;
        Darwin) os="darwin" ;;
        *)      error "Unsupported OS: $(uname -s)" ;;
    esac

    case "$(uname -m)" in
        x86_64|amd64)   arch="amd64" ;;
        aarch64|arm64)  arch="arm64" ;;
        *)              error "Unsupported architecture: $(uname -m)" ;;
    esac

    echo "${os}_${arch}"
}

get_latest_version() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
        grep '"tag_name"' |
        sed -E 's/.*"([^"]+)".*/\1/'
}

build_from_source() {
    info "Falling back to build from source..."
    if ! command -v go &>/dev/null; then
        error "Go is not installed. Install Go 1.22+ or use a prebuilt binary."
    fi

    local go_version
    go_version="$(go version | grep -oE 'go[0-9]+\.[0-9]+' | sed 's/go//')"
    info "Go version: ${go_version}"

    local tmp_dir
    tmp_dir="$(mktemp -d)"
    trap 'rm -rf "${tmp_dir}"' EXIT

    info "Cloning repository..."
    git clone --depth 1 "https://github.com/${REPO}.git" "${tmp_dir}/stoke"

    info "Building..."
    (cd "${tmp_dir}/stoke" && go build -trimpath -ldflags="-s -w" -o "${tmp_dir}/stoke-bin" ./cmd/stoke)
    (cd "${tmp_dir}/stoke" && go build -trimpath -ldflags="-s -w" -o "${tmp_dir}/stoke-acp-bin" ./cmd/stoke-acp)

    for pair in "${tmp_dir}/stoke-bin:${BINARY}" "${tmp_dir}/stoke-acp-bin:stoke-acp"; do
        src="${pair%%:*}"
        dst_name="${pair##*:}"
        dst="${INSTALL_DIR}/${dst_name}"
        if [ -w "${INSTALL_DIR}" ]; then
            cp "${src}" "${dst}"
        else
            sudo cp "${src}" "${dst}"
        fi
        chmod +x "${dst}"
    done

    info "Built and installed stoke to ${INSTALL_DIR}/${BINARY}"
    info "Built and installed stoke-acp (Agent Client Protocol adapter) to ${INSTALL_DIR}/stoke-acp"
    info "Run 'stoke doctor' to verify your setup."
}

main() {
    local platform version archive_name url checksum_url tmp_dir

    info "Detecting platform..."
    platform="$(detect_platform)"
    info "Platform: ${platform}"

    info "Finding latest release..."
    version="${VERSION:-$(get_latest_version 2>/dev/null || true)}"
    if [ -z "${version}" ]; then
        info "No release found. Building from source."
        build_from_source
        return
    fi
    info "Version: ${version}"

    # Strip leading 'v' for archive name
    local ver_num="${version#v}"
    archive_name="stoke_${ver_num}_${platform}.tar.gz"
    url="https://github.com/${REPO}/releases/download/${version}/${archive_name}"
    checksum_url="https://github.com/${REPO}/releases/download/${version}/checksums.txt"

    tmp_dir="$(mktemp -d)"
    trap 'rm -rf "${tmp_dir}"' EXIT

    info "Downloading ${url}..."
    if ! curl -fsSL -o "${tmp_dir}/${archive_name}" "${url}"; then
        info "Prebuilt binary not available for ${platform}. Building from source."
        build_from_source
        return
    fi

    info "Verifying checksum..."
    curl -fsSL -o "${tmp_dir}/checksums.txt" "${checksum_url}"
    (cd "${tmp_dir}" && grep "${archive_name}" checksums.txt | sha256sum -c --quiet) ||
        error "Checksum verification failed!"

    # Verify cosign signature if cosign is available.
    # Keyless verification (cosign v2+) requires pinning the signer
    # identity to this repo's release workflow on a tag ref. The regex
    # allows any tag under refs/tags/* so new releases verify without
    # script edits, but blocks signers from other repos/workflows.
    if command -v cosign &>/dev/null; then
        local sig_url="${url}.sig"
        info "Verifying cosign signature..."
        if curl -fsSL -o "${tmp_dir}/${archive_name}.sig" "${sig_url}" 2>/dev/null; then
            if cosign verify-blob \
                --certificate-identity-regexp "https://github\.com/ericmacdougall/Stoke/\.github/workflows/release\.yml@refs/tags/.*" \
                --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
                --signature "${tmp_dir}/${archive_name}.sig" \
                "${tmp_dir}/${archive_name}" 2>&1; then
                info "Signature verified (keyless, pinned to release.yml)."
            else
                error "Cosign signature verification FAILED. The binary may have been tampered with."
            fi
        else
            info "Signature file not available (pre-release?). Skipping signature verification."
        fi
    else
        info "cosign not installed; skipping signature verification. Install cosign to verify: https://docs.sigstore.dev/cosign/system_config/installation/"
    fi

    info "Installing to ${INSTALL_DIR}..."
    tar -xzf "${tmp_dir}/${archive_name}" -C "${tmp_dir}"

    # Install both binaries from the release archive: the main
    # stoke CLI and the stoke-acp Agent Client Protocol adapter
    # (S-U-002). The ACP adapter is optional — older archives
    # won't include it, so missing stoke-acp isn't a hard error.
    install_one() {
        local bin_name="$1"
        local dest_name="$2"
        local required="$3"
        local found
        found="$(find "${tmp_dir}" -name "${bin_name}" -type f -perm -u+x | head -1)"
        if [ -z "${found}" ]; then
            found="$(find "${tmp_dir}" -name "${bin_name}" -type f | head -1)"
        fi
        if [ -z "${found}" ]; then
            if [ "${required}" = "required" ]; then
                error "Could not find ${bin_name} binary in archive"
            fi
            info "Optional binary ${bin_name} not in this release archive; skipping."
            return
        fi
        if [ -w "${INSTALL_DIR}" ]; then
            cp "${found}" "${INSTALL_DIR}/${dest_name}"
            chmod +x "${INSTALL_DIR}/${dest_name}"
        else
            info "Need sudo to install ${dest_name} to ${INSTALL_DIR}"
            sudo cp "${found}" "${INSTALL_DIR}/${dest_name}"
            sudo chmod +x "${INSTALL_DIR}/${dest_name}"
        fi
        info "Installed ${dest_name} ${version} to ${INSTALL_DIR}/${dest_name}"
    }

    install_one stoke "${BINARY}" required
    install_one stoke-acp stoke-acp optional

    info "Run 'stoke doctor' to verify your setup."
}

main "$@"
