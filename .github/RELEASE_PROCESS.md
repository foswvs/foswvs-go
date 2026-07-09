# Release Process

This document explains how to create and publish a new release of foswvs-go.

## Prerequisites

- GitHub repository with push access
- Git installed locally
- Tag format: `v*` (e.g., `v1.0.0`, `v1.2.3`)

## Creating a Release

### Step 1: Prepare your commits

Ensure all changes for this release are committed to `main`:

```bash
git log --oneline -n 10
```

### Step 2: Create a version tag

Tags trigger the automated release workflow:

```bash
# Create a new tag (semantic versioning)
git tag v1.0.0
# OR with a message
git tag -a v1.0.0 -m "Release version 1.0.0"
```

Common patterns:
- `v1.0.0` — initial release
- `v1.1.0` — new features (minor version bump)
- `v1.0.1` — bug fix (patch version bump)
- `v2.0.0` — breaking changes (major version bump)

### Step 3: Push the tag

This triggers the GitHub Actions workflow:

```bash
git push origin v1.0.0
```

Push all tags:

```bash
git push origin --tags
```

## Automated Workflow

The `.github/workflows/release.yml` workflow will automatically:

1. **Build binaries**
   - Linux ARM64 (64-bit Raspberry Pi)
   - Linux ARMv7 (32-bit Raspberry Pi)

2. **Create checksums**
   - SHA256 checksums for all binaries in `checksums.txt`

3. **Generate release notes**
   - Automatic changelog from commit history

4. **Create GitHub Release**
   - All binaries attached
   - Checksums included
   - Release notes populated

## Monitoring the Release

### Via GitHub UI

1. Go to your repository
2. Click "Actions" tab
3. Look for the workflow named "Release"
4. Click the workflow run to see progress

### View the Release

1. Go to "Releases" tab
2. See the newly created release with all binaries

### Direct link

```
https://github.com/foswvs/foswvs-go/releases/tag/v1.0.0
```

## Distributing the Release

### One-liner installation

Share this command with users:

```bash
curl -sSfL https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash
```

### Specific version

Users can install a specific version:

```bash
curl -sSfL https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash -s v1.0.0
```

### Documentation

Add release notes to your announcement:
- Bug fixes included
- New features
- Breaking changes (if any)
- Known issues

Example:

```
foswvs-go v1.0.0 - Initial stable release

Features:
- Complete WiFi captive portal with coin acceptor support
- Real-time WebSocket updates for data usage
- Automatic device tracking via DHCP/ARP
- Admin dashboard for management

Installation:
curl -sSfL https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash
```

## Build Contents

Each release includes:

```
foswvs-go_linux_arm64.tar.gz         (2-4 MB)
├── foswvs-go                        (binary)
├── README.md
├── INSTALL.md
└── LICENSE

foswvs-go_linux_armv7.tar.gz         (2-4 MB)
├── foswvs-go                        (binary)
├── README.md
├── INSTALL.md
└── LICENSE

checksums.txt                         (<1 KB)
├── SHA256 hashes for all binaries
```

## Version Naming

Follow Semantic Versioning (https://semver.org/):

- **MAJOR**: Breaking changes — `v2.0.0`
- **MINOR**: New features, backwards-compatible — `v1.1.0`
- **PATCH**: Bug fixes only — `v1.0.1`

Examples:
- `v1.0.0` — first release
- `v1.0.1` — security patch
- `v1.1.0` — new features added
- `v2.0.0` — breaking changes (incompatible configs, etc.)

## Pre-releases

To release a beta or release candidate:

```bash
git tag v1.0.0-beta.1
git push origin v1.0.0-beta.1
```

The workflow will mark this as a "pre-release" on GitHub. Users must opt-in to install pre-releases:

```bash
curl -sSfL https://raw.githubusercontent.com/foswvs/foswvs-go/main/install.sh | bash -s v1.0.0-beta.1
```

## Troubleshooting

### Release workflow fails

1. Check the Actions log:
   ```
   https://github.com/foswvs/foswvs-go/actions
   ```

2. Common issues:
   - Go version mismatch (check `.github/workflows/release.yml`)
   - Missing `GITHUB_TOKEN` secret (should be automatic)
   - Compilation errors (fix in code, push new commit, retag)

### Binary not found in release

- Verify the tag format starts with `v` (case-sensitive)
- Ensure the workflow completed successfully
- Wait a minute for GitHub to refresh the release page

### Wrong binary included

- Rebuild: delete the tag and recreate it
  ```bash
  git tag -d v1.0.0
  git push origin :v1.0.0  # Delete remote tag
  git tag v1.0.0
  git push origin v1.0.0
  ```

## Rollback

If you need to unpublish a release:

```bash
# Delete the tag locally
git tag -d v1.0.0

# Delete it on GitHub
git push origin :v1.0.0

# Delete the GitHub Release via UI if needed
```

## Testing locally

To test the build without creating a release:

```bash
# Install goreleaser if needed
brew install goreleaser  # macOS
# OR
https://goreleaser.com/install/

# Build locally (creates dist/ directory)
goreleaser build --snapshot --clean

# Test the binary
./dist/foswvs-go_linux_arm64/foswvs-go -version
```

## Release Checklist

Before creating a release:

- [ ] All commits pushed to `main`
- [ ] Tests pass (if applicable)
- [ ] README updated with latest info
- [ ] RELEASES.md up-to-date
- [ ] Version bumped (if using version file)
- [ ] Release notes prepared

When creating release:

- [ ] Tag pushed to GitHub
- [ ] Actions workflow completes successfully
- [ ] All binaries present in release
- [ ] Checksums verified
- [ ] Release notes are clear and complete

After release:

- [ ] Announcement posted (GitHub Discussions, etc.)
- [ ] Installation link shared
- [ ] Users notified of new version
