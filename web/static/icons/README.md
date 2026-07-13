# PWA Icons Directory

This directory contains icons for the Progressive Web App (PWA). The app will not install properly without these icon files.

## Generating Icons

### Option 1: Automated Generation (Recommended)

Requires Node.js and npm:

```bash
npm install sharp
node scripts/generate-pwa-icons.js
```

This generates all icons automatically from the SVG template.

### Option 2: Online Tools

Use the SVG template (`icon-template.svg`) with any online PWA icon generator:

1. **PWA Builder** — https://www.pwabuilder.com/
2. **Favicon Generator** — https://www.favicon-generator.org/
3. **Maskable Icon Editor** — https://maskable.app/

Simply upload `icon-template.svg` and download the generated icons.

### Option 3: Command Line (ImageMagick)

If you have ImageMagick installed:

```bash
# Generate regular icons
for size in 72 96 128 144 152 192 384 512; do
  convert icon-template.svg -resize ${size}x${size} icon-${size}x${size}.png
done

# Generate maskable icons
for size in 192 512; do
  convert icon-template.svg -resize ${size}x${size} icon-${size}x${size}-maskable.png
done

# Generate shortcut icon
cp icon-96x96.png shortcut-96x96.png

# Generate screenshots
convert -size 540x720 xc:white -pointsize 48 -fill "#0d9488" -gravity center -annotate +0+0 "PISO WIFI" screenshot-1.png
convert -size 1280x720 xc:white -pointsize 48 -fill "#0d9488" -gravity center -annotate +0+0 "PISO WIFI" screenshot-2.png
```

### Option 4: Docker

If Docker is available:

```bash
docker run --rm -v $(pwd):/work -w /work node:18 bash -c "
  npm install sharp && node scripts/generate-pwa-icons.js
"
```

## Files Required

The following icons are required for full PWA support:

### Regular Icons (any/purpose)
- `icon-72x72.png`
- `icon-96x96.png`
- `icon-128x128.png`
- `icon-144x144.png`
- `icon-152x152.png`
- `icon-192x192.png`
- `icon-384x384.png`
- `icon-512x512.png`

### Maskable Icons (Android adaptive)
- `icon-192x192-maskable.png`
- `icon-512x512-maskable.png`

### Shortcuts
- `shortcut-96x96.png`

### Screenshots
- `screenshot-1.png` (540x720)
- `screenshot-2.png` (1280x720)

## Customizing Icons

### Change Colors

Edit `icon-template.svg`:

```svg
<!-- Change background circle color -->
<circle cx="256" cy="256" r="256" fill="#YOUR-COLOR-HEX"/>

<!-- Change symbol color -->
<g ... stroke="#YOUR-SYMBOL-COLOR-HEX">
```

### Use Your Own Logo

Replace the WiFi symbol with your own design:

1. Open `icon-template.svg` in an editor
2. Replace the `<g>` element with your logo
3. Keep viewBox="0 0 512 512"
4. Use white/light colors for contrast
5. Regenerate icons

### Adaptive Icons for Android

Maskable icons should have:
- Transparent background
- Solid color symbol
- Extra padding (safe zone)
- At least 200px inner diameter for content

Example (`icon-512x512-maskable.png`):
- Should show symbol clearly when masked to circular shape
- Safe content area: center 260px circle

## Verifying Installation

After generating icons:

1. **Check files exist:**
   ```bash
   ls -lh web/static/icons/icon-*.png
   ```

2. **Test in browser:**
   - Open http://10.0.0.1/
   - Chrome DevTools → Application → Manifest
   - Verify all icon paths point to existing files

3. **Test on mobile:**
   - Try to install app
   - Check if app icon displays correctly
   - Verify splash screen appears with logo

## Troubleshooting

### "Icons not showing in manifest"

- Verify all PNG files exist in this directory
- Check manifest.json paths are correct
- Ensure files are world-readable: `chmod 644 *.png`

### "App won't install on mobile"

- Verify at least `icon-192x192.png` exists
- Check icon files are valid PNG format: `file *.png`
- Try `icon-512x512.png` as fallback
- Clear browser cache and reload

### "Icon looks pixelated"

- Regenerate at larger size (512x512 minimum)
- Use high-quality SVG template
- Ensure sharp is properly configured

### "Maskable icons not showing correctly"

- Check Android version (requires Android 8+)
- Verify maskable icon has transparent background
- Use the dedicated `*-maskable.png` files

## Icon Sizes Explained

| Size | Usage | Quality |
|------|-------|---------|
| 72   | Legacy Android, Windows tiles | Low |
| 96   | Shortcuts, app launcher | Medium |
| 128  | Fallback | Medium |
| 144  | Windows tiles, taskbar | High |
| 152  | iPad, app install | High |
| 192  | Primary Android icon, install | High |
| 384  | App stores, large displays | Very High |
| 512  | Splash screen, app manifest | Very High |

Larger files provide better quality on modern devices.

## Further Reading

- [MDN: Web App Manifest Icons](https://developer.mozilla.org/en-US/docs/Web/Manifest/icons)
- [Android Adaptive Icons](https://developer.android.com/guide/practices/ui_guidelines/icon_design_adaptive)
- [Apple App Icon Design](https://developer.apple.com/design/human-interface-guidelines/app-icons)
- [PWA Builder Icon Generator](https://www.pwabuilder.com/)
