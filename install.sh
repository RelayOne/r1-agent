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

    # Verify cosign signature if cosign is available
    if command -v cosign &>/dev/null; then
        local sig_url="${url}.sig"
        info "Verifying cosign signature..."
        if curl -fsSL -o "${tmp_dir}/${archive_name}.sig" "${sig_url}" 2>/dev/null; then
            # Signature file downloaded — verification MUST pass
            if cosign verify-blob \
                --signature "${tmp_dir}/${archive_name}.sig" \
                "${tmp_dir}/${archive_name}" 2>/dev/null; then
                info "Signature verified."
            else
                error "Cosign signature verification FAILED. The binary may have been tampered with."
            fi
        else
            info "Signature file not available (pre-release?). Skipping signature verification."
        fi
    fi

    info "Installing to ${INSTALL_DIR}..."
    tar -xzf "${tmp_dir}/${archive_name}" -C "${tmp_dir}"

    # Find the binary in the extracted archive
    local bin_path
    bin_path="$(find "${tmp_dir}" -name stoke -type f -perm -u+x | head -1)"
    if [ -z "${bin_path}" ]; then
        bin_path="$(find "${tmp_dir}" -name stoke -type f | head -1)"
    fi
    [ -n "${bin_path}" ] || error "Could not find stoke binary in archive"

    if [ -w "${INSTALL_DIR}" ]; then
        cp "${bin_path}" "${INSTALL_DIR}/${BINARY}"
        chmod +x "${INSTALL_DIR}/${BINARY}"
    else
        info "Need sudo to install to ${INSTALL_DIR}"
        sudo cp "${bin_path}" "${INSTALL_DIR}/${BINARY}"
        sudo chmod +x "${INSTALL_DIR}/${BINARY}"
    fi

    info "Installed stoke ${version} to ${INSTALL_DIR}/${BINARY}"
    info "Run 'stoke doctor' to verify your setup."
}

main "$@"
