# foswvs-go Releases

This guide explains how to download and install foswvs-go using GitHub Releases.

## Quick Install

### One-liner (all platforms)

```bash
curl -sSfL https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash
```

### Specific version

```bash
curl -sSfL https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash -s v1.2.3
```

### Custom installation directory (user-local)

```bash
export INSTALL_DIR=~/.local/bin
curl -sSfL https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash
```

### Manual install

1. Visit [GitHub Releases](https://github.com/foswvs/foswvs-go/releases)
2. Download the binary for your platform:
   - Raspberry Pi (ARMv7, 32-bit): `foswvs-go_linux_armv7.tar.gz`
   - Raspberry Pi (ARM64, 64-bit): `foswvs-go_linux_arm64.tar.gz`
3. Extract and install:
   ```bash
   tar -xzf foswvs-go_linux_*.tar.gz
   sudo cp foswvs-go /usr/local/bin/
   foswvs-go -version  # Verify
   ```

## What's included in each release

Each release includes:

- **Binary archive** (tar.gz)
  - `foswvs-go` — the compiled executable
  - `README.md` — project overview
  - `INSTALL.md` — detailed installation and configuration guide
  - `LICENSE` — project license

- **Checksums** (`checksums.txt`)
  - SHA256 hashes for all binaries
  - Automatically verified by `install.sh`

## Supported platforms

| Platform | Binary |
|----------|--------|
| Raspberry Pi 2/3/4 (32-bit) | `foswvs-go_linux_armv7.tar.gz` |
| Raspberry Pi 3/4 (64-bit) | `foswvs-go_linux_arm64.tar.gz` |

## Verifying downloads

The installer script automatically verifies checksums. To manually verify:

```bash
sha256sum -c checksums.txt
```

## Installation directory

By default, `install.sh` installs to `/usr/local/bin`. To use a different location:

```bash
INSTALL_DIR=~/.local/bin bash install.sh
```

Make sure your chosen directory is in your `$PATH`:

```bash
echo $PATH
```

If it's not, add it to your shell profile (`.bashrc`, `.zshrc`, etc.):

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Updating

Re-run the installer to update to the latest version:

```bash
bash install.sh
```

Or specify a version:

```bash
bash install.sh v1.2.3
```

## Troubleshooting

### "unsupported architecture"

The binary isn't available for your platform. Check:

```bash
uname -s     # OS (Linux, Darwin, etc.)
uname -m     # Architecture (aarch64, armv7l, etc.)
```

Then visit [Releases](https://github.com/foswvs/foswvs-go/releases) to see available binaries.

### "Permission denied"

The installation directory isn't writable. Either:

1. Use `sudo`:
   ```bash
   sudo bash install.sh
   ```

2. Or install to a user directory:
   ```bash
   INSTALL_DIR=~/.local/bin bash install.sh
   ```

### "Checksum mismatch"

Your download was corrupted. Try again:

```bash
bash install.sh
```

Or delete the temporary files and retry.

### "curl: command not found"

Use `wget` instead:

```bash
wget -qO- https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash
```

## Building from source

If no binary is available for your platform, build it yourself:

```bash
git clone https://github.com/foswvs/foswvs-go.git
cd foswvs-go
make build           # Current platform
make build-arm       # Raspberry Pi (32-bit)
make build-arm64     # Raspberry Pi (64-bit)
```

See [INSTALL.md](INSTALL.md) for full setup instructions.

## Configuration after install

After installing the binary, follow [INSTALL.md](INSTALL.md) for:

- Setting up the wireless access point (hostapd)
- Configuring DHCP server
- Setting up the systemd service
- First-time login and admin setup

## Release versioning

Releases follow [Semantic Versioning](https://semver.org/):

- `v1.0.0` — major version (breaking changes)
- `v1.2.3` — minor version (new features)
- `v1.2.3` — patch version (bug fixes)

Pre-release versions (e.g., `v1.0.0-beta.1`) are marked as pre-releases on GitHub.

## Release notes

Each release includes detailed release notes explaining:

- New features
- Bug fixes
- Breaking changes (if any)
- Known issues

Read them before updating to understand what's changed.
