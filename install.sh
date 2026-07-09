#!/usr/bin/env bash
# foswvs-go installer script
# Downloads and installs foswvs-go binary with checksum verification

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
GITHUB_REPO="foswvs/foswvs-go"
BINARY_NAME="foswvs-go"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${1:-latest}"

log_info() {
    echo -e "${BLUE}ℹ${NC} $*"
}

log_success() {
    echo -e "${GREEN}✓${NC} $*"
}

log_error() {
    echo -e "${RED}✗${NC} $*" >&2
}

log_warn() {
    echo -e "${YELLOW}⚠${NC} $*"
}

# Detect OS and architecture
detect_platform() {
    local os arch

    case "$(uname -s)" in
        Linux*)  os="linux" ;;
        Darwin*) os="darwin" ;;
        *)       log_error "Unsupported OS: $(uname -s)"; exit 1 ;;
    esac

    case "$(uname -m)" in
        aarch64|arm64)  arch="arm64" ;;
        armv7l|armv6l)  arch="arm" ;;
        x86_64)         arch="amd64" ;;
        *)              log_error "Unsupported architecture: $(uname -m)"; exit 1 ;;
    esac

    # Add ARM version suffix if on 32-bit ARM
    if [[ "$arch" == "arm" ]]; then
        arch="armv7"
    fi

    echo "${os}_${arch}"
}

# Get download URL for the latest or specified release
get_download_url() {
    local platform="$1"
    local version="$2"
    local release_info

    if [[ "$version" == "latest" ]]; then
        log_info "Fetching latest release info..."
        release_info=$(curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases/latest")
    else

        log_info "Fetching release info for ${version}..."
        release_info=$(curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases/tags/${version}")
    fi

    # Check if we got valid JSON
    if ! echo "$release_info" | grep -q '"download_url"'; then
        log_error "Failed to fetch release info. Check your internet connection or GitHub API limits."
        exit 1
    fi

    # Extract the download URL for our platform
    local binary_file="${BINARY_NAME}_${platform}.tar.gz"
    local download_url=$(echo "$release_info" | grep -oP '"browser_download_url": "\K[^"]*'"${binary_file}"'[^"]*' | head -1)

    if [[ -z "$download_url" ]]; then
        log_error "Binary not found for platform: ${platform}"
        echo "Available binaries:"
        echo "$release_info" | grep -oP '"name": "\K[^"]*' | grep "\.tar\.gz"
        exit 1
    fi

    echo "$download_url"
}

# Verify checksum
verify_checksum() {
    local file="$1"
    local expected_checksum="$2"

    log_info "Verifying checksum..."

    local actual_checksum
    if command -v sha256sum &> /dev/null; then
        actual_checksum=$(sha256sum "$file" | cut -d' ' -f1)
    elif command -v shasum &> /dev/null; then
        actual_checksum=$(shasum -a 256 "$file" | cut -d' ' -f1)
    else
        log_warn "sha256sum/shasum not found, skipping checksum verification"
        return 0
    fi

    if [[ "$actual_checksum" != "$expected_checksum" ]]; then
        log_error "Checksum mismatch!"
        log_error "Expected: ${expected_checksum}"
        log_error "Got:      ${actual_checksum}"
        return 1
    fi

    log_success "Checksum verified"
    return 0
}

# Download file with progress
download_file() {
    local url="$1"
    local output="$2"

    log_info "Downloading from ${url}..."

    if ! curl -fL --progress-bar -o "$output" "$url"; then
        log_error "Download failed"
        rm -f "$output"
        exit 1
    fi

    log_success "Download complete"
}

# Main installation
main() {
    log_info "foswvs-go installer"

    # Check if running with required permissions if installing to system directory
    if [[ "$INSTALL_DIR" == "/usr/local/bin" ]] || [[ "$INSTALL_DIR" == "/usr/bin" ]]; then
        if [[ ! -w "$INSTALL_DIR" ]]; then
            log_warn "Installation directory is not writable. You may need to use sudo."
            if ! sudo -n true 2>/dev/null; then
                log_error "This installation requires sudo. Run with 'sudo bash install.sh' or set INSTALL_DIR to a user-writable location."
                exit 1
            fi
            SUDO_PREFIX="sudo"
        fi
    fi

    # Detect platform
    local platform
    platform=$(detect_platform)
    log_info "Detected platform: ${platform}"

    # Create temporary directory
    local tmpdir
    tmpdir=$(mktemp -d)
    trap "rm -rf $tmpdir" EXIT

    log_info "Using temporary directory: ${tmpdir}"
    cd "$tmpdir"

    # Get download URL
    local download_url checksums_url
    download_url=$(get_download_url "$platform" "$VERSION")
    checksums_url=$(echo "$download_url" | sed 's/\.tar\.gz$//' | sed 's/'"${BINARY_NAME}"'_[^/]*\///' | xargs -I {} echo "https://github.com/${GITHUB_REPO}/releases/download/{}/checksums.txt")

    # For GitHub releases, checksums are at a predictable path
    local release_tag
    release_tag=$(basename $(dirname "$download_url"))
    checksums_url="https://github.com/${GITHUB_REPO}/releases/download/${release_tag}/checksums.txt"

    # Download binary and checksums
    local binary_file
    binary_file=$(basename "$download_url")

    download_file "$download_url" "$binary_file"
    download_file "$checksums_url" "checksums.txt"

    # Extract checksum for our binary
    local expected_checksum
    if expected_checksum=$(grep "$binary_file" checksums.txt | cut -d' ' -f1); then
        verify_checksum "$binary_file" "$expected_checksum"
    else
        log_warn "Checksum for ${binary_file} not found in checksums.txt"
    fi

    # Extract binary
    log_info "Extracting binary..."
    tar -xzf "$binary_file"

    if [[ ! -f "$BINARY_NAME" ]]; then
        log_error "Binary not found after extraction"
        exit 1
    fi

    chmod +x "$BINARY_NAME"
    log_success "Binary extracted and made executable"

    # Install binary
    log_info "Installing to ${INSTALL_DIR}/${BINARY_NAME}..."

    if [[ -n "${SUDO_PREFIX:-}" ]]; then
        $SUDO_PREFIX cp "$BINARY_NAME" "${INSTALL_DIR}/${BINARY_NAME}"
        $SUDO_PREFIX chmod 755 "${INSTALL_DIR}/${BINARY_NAME}"
    else
        cp "$BINARY_NAME" "${INSTALL_DIR}/${BINARY_NAME}"
        chmod 755 "${INSTALL_DIR}/${BINARY_NAME}"
    fi

    log_success "Installation complete!"

    # Verify installation
    if command -v "$BINARY_NAME" &> /dev/null; then
        local installed_version
        installed_version=$("$BINARY_NAME" -version 2>/dev/null || echo "unknown")
        log_info "Installed version: ${installed_version}"
    fi

    # Print next steps
    echo ""
    log_info "Next steps:"
    echo "  1. Review the installation guide: https://github.com/${GITHUB_REPO}/blob/main/INSTALL.md"
    echo "  2. If you need to install on a Raspberry Pi, follow the setup instructions in INSTALL.md"
    echo "  3. For updates: re-run this installer script"
    echo ""
    echo "Usage:"
    echo "  ${BINARY_NAME} -help"
}

# Print usage
usage() {
    cat << EOF
foswvs-go installer

Usage: $0 [VERSION]

Arguments:
  VERSION   Release tag to install (default: latest)
            Examples: latest, v1.0.0, v1.2.3

Environment variables:
  INSTALL_DIR    Installation directory (default: /usr/local/bin)

Examples:
  bash install.sh                 # Install latest release
  bash install.sh v1.0.0          # Install specific version
  INSTALL_DIR=~/.local/bin bash install.sh  # Install to user directory

EOF
}

# Handle --help
if [[ "${1:-}" == "--help" ]] || [[ "${1:-}" == "-h" ]]; then
    usage
    exit 0
fi

main
