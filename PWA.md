# Progressive Web App (PWA) Setup

foswvs-go is configured as a Progressive Web App, enabling offline functionality, app-like installation, and improved mobile experience.

## What is a PWA?

A Progressive Web App combines the best of web and mobile apps:

- **Installable** — Add to home screen on iOS/Android
- **Offline-capable** — Works without internet using cached assets
- **App-like** — Full-screen, custom splash screen, app icon
- **Fast** — Loads instantly from cache
- **Responsive** — Adapts to any screen size

## Features

### ✓ Service Worker
- Caches static assets (CSS, JS, images) on first load
- Handles offline gracefully with fallback pages
- Network-first strategy for API calls (real-time data)
- Cache-first strategy for static assets (fast loading)
- Automatic cache cleanup

### ✓ Web App Manifest
- App name, description, and icons
- Launcher shortcuts (Connect WiFi, Admin Dashboard)
- Splash screens on mobile
- Share target API (future enhancement)

### ✓ iOS Support
- Standalone mode (hides browser chrome)
- Custom status bar
- App icon for home screen
- Splash screen support

### ✓ Android Support
- Adaptive icons (maskable icons for modern Android)
- Install promotion
- Full app shell
- Hardware back button handling

## Installation

### On Android

1. Open foswvs-go portal (`http://10.0.0.1`) in Chrome
2. Chrome will show "Install app" prompt at bottom
3. Tap "Install" → "Install" again
4. App appears on home screen

Or manually:
1. Open Chrome menu (⋮)
2. Tap "Install app"
3. Confirm installation

### On iOS

1. Open foswvs-go portal in Safari
2. Tap Share button (↗)
3. Tap "Add to Home Screen"
4. Name the app (default: "PisoWiFi")
5. Tap "Add"

Or for Admin Dashboard:
1. Open `http://10.0.0.1/a/` in Safari
2. Follow same steps (default: "PisoWiFi Admin")

## Files

### manifest.json
Web app metadata including:
- App name and description
- Display mode (standalone = full screen)
- Theme colors for browser/status bar
- Icons in various sizes
- Shortcuts for quick actions
- Screenshots for app stores

**Location:** `/web/static/manifest.json`

### sw.js (Service Worker)
Handles:
- Offline caching strategy
- Dynamic cache busting
- API request handling
- Fallback pages
- Background sync (future)

**Location:** `/web/static/sw.js`

### icons/
Generated PWA icons in various sizes:
- `icon-*.png` — Regular icons (72px to 512px)
- `icon-*-maskable.png` — Adaptive icons for Android
- `screenshot-*.png` — Store screenshots
- `shortcut-*.png` — Launcher shortcuts

**Location:** `/web/static/icons/`

## Generating Icons

### Quick Start (Automated)

Requires Node.js and npm:

```bash
npm install sharp
node scripts/generate-pwa-icons.js
```

This generates all required icons from the SVG template.

### Manual Generation

If `sharp` isn't available, you can generate icons using online tools:

1. **SVG Template** — Use `web/static/icons/icon-template.svg`
2. **Export sizes:**
   - 72x72, 96x96, 128x128, 144x144, 152x152, 192x192, 384x384, 512x512 (regular)
   - 192x192-maskable, 512x512-maskable (for Android adaptive icons)
3. **Save as PNG** to `/web/static/icons/icon-SIZEx SIZE.png`

**Online tools:**
- [Figma PWA Icon Generator](https://www.figma.com/)
- [Favicon Generator](https://www.favicon-generator.org/)
- [PWA Asset Generator](https://www.npmjs.com/package/pwa-asset-generator)
- [ImageMagick](https://imagemagick.org/)

```bash
# Example with ImageMagick:
convert -background none icon-template.svg -resize 192x192 icons/icon-192x192.png
```

### Custom Logo

To replace the WiFi icon with your own logo:

1. Create or export a square PNG/SVG image
2. Recommended: use a bright color on transparent background
3. Use online tools or ImageMagick to generate all sizes
4. Place in `/web/static/icons/`

## Testing

### Desktop (Chrome)

1. Open DevTools (F12)
2. Go to Application → Manifest
3. Check manifest is loaded and valid
4. Go to Application → Service Workers
5. Verify service worker is registered and active

### Mobile (Android Chrome)

1. Open chrome://flags
2. Enable "Parallel downloading"
3. Open foswvs-go portal
4. Menu (⋮) → Install app

### Mobile (iOS Safari)

1. Open Safari
2. Open foswvs-go portal
3. Share (↗) → Add to Home Screen
4. Launch app from home screen

### Offline Testing

1. Install the app on mobile
2. Go to DevTools → Application → Service Workers
3. Check "Offline"
4. Reload the page
5. Verify it loads cached content

## Customization

### Change App Name

Edit `manifest.json`:

```json
{
  "name": "Your Custom Name",
  "short_name": "Custom"
}
```

### Change Theme Colors

Edit both `manifest.json` and HTML files:

```json
{
  "theme_color": "#your-color-hex",
  "background_color": "#your-bg-hex"
}
```

**HTML files** (`web/static/index.html`, `web/static/a/index.html`):

```html
<meta name="theme-color" content="#your-color-hex">
```

### Add Custom Shortcuts

Edit `manifest.json` `shortcuts` array:

```json
"shortcuts": [
  {
    "name": "Your Action",
    "short_name": "Action",
    "description": "Description",
    "url": "/your-path",
    "icons": [{"src": "/icons/icon-96x96.png", "sizes": "96x96"}]
  }
]
```

### Disable Offline Support

Edit `sw.js` and remove/comment out caching logic, or don't register the service worker in HTML files.

## Debugging

### Chrome DevTools

**Application tab:**
- Manifest — check if valid
- Service Workers — check registration, active status
- Cache Storage — view cached files
- Local Storage — debug app state

**Console errors:**
- Service worker registration failures
- Network request errors during offline

### Common Issues

**Service Worker won't register:**
- Check browser console for errors
- Verify SW file exists at `/sw.js`
- Ensure HTTPS or localhost (except 127.0.0.1)
- Clear site data and reload

**Icons not showing:**
- Verify icon files exist in `/web/static/icons/`
- Check image format is PNG with correct size
- Try clearing cache and reinstalling app
- Verify manifest paths are correct

**App won't install:**
- Check manifest is valid (Chrome DevTools)
- Ensure HTTPS (except on localhost)
- Verify app name and icons are set
- Try different browser (Chrome/Edge work best)

**Offline page shows errors:**
- Verify `sw.js` is active and not erroring
- Check cache contents in DevTools
- Ensure key pages are in STATIC_ASSETS list

## Performance

### Cache Sizes

- Initial cache: ~1-2 MB (HTML, CSS, JS)
- Runtime cache: varies (API responses cached)
- Clean up old caches automatically on activation

### Network Usage

- **Online:** Minimal — cached assets load instantly
- **Offline:** Zero network — works entirely from cache
- **Connectivity loss:** Graceful degradation with offline fallback

### Load Times

- **With cache:** < 1 second (even on slow networks)
- **Cold load:** Depends on network, typical 2-5 seconds
- **Service worker:** ~100ms startup time

## Future Enhancements

### Planned Features

- **Background Sync** — Queue failed share transactions, retry when online
- **Periodic Sync** — Check for updates in background
- **Web Share API** — Share WiFi codes with native sharing
- **Push Notifications** — Server push for important alerts
- **Sync Data Storage** — IndexedDB for larger caches

### Implementation

Enable in `manifest.json`:

```json
{
  "share_target": {
    "action": "/share",
    "method": "POST",
    "params": {"title": "title", "text": "text"}
  }
}
```

Handle in service worker:

```javascript
self.addEventListener('sync', event => {
  if (event.tag === 'sync-share-tx') {
    // Retry failed transactions
  }
});
```

## Compliance

### Web Standards

- ✓ W3C Web App Manifest
- ✓ WHATWG Service Worker spec
- ✓ Serviceworker compatible browsers
- ✓ Fallback for older browsers (graceful degradation)

### Browser Support

| Browser | Version | Support |
|---------|---------|---------|
| Chrome  | 39+     | ✓ Full  |
| Firefox | 44+     | ✓ Full  |
| Safari  | 11.1+   | ~ Partial (install via Share) |
| Edge    | 17+     | ✓ Full  |
| Opera   | 26+     | ✓ Full  |

**Note:** iOS Safari has limited PWA support (no install prompt in browser UI, manual "Add to Home Screen" required).

## Resources

### Documentation

- [MDN Web Docs - Progressive Web Apps](https://developer.mozilla.org/en-US/docs/Web/Progressive_web_apps)
- [Google Web - PWA](https://web.dev/progressive-web-apps/)
- [W3C Web App Manifest](https://www.w3.org/TR/appmanifest/)
- [Service Worker API](https://developer.mozilla.org/en-US/docs/Web/API/Service_Worker_API)

### Tools

- [PWA Builder](https://www.pwabuilder.com/) — Generate manifest, icons, etc.
- [Lighthouse](https://developers.google.com/web/tools/lighthouse) — PWA audit
- [Maskable Icon Editor](https://maskable.app/)
- [Sharp (image processing)](https://sharp.pixelplumbing.com/)

### Testing

- [Chrome DevTools](https://developer.chrome.com/docs/devtools/)
- [WebPageTest PWA Test](https://www.webpagetest.org/)
- [BrowserStack](https://www.browserstack.com/) — Real device testing

## Deployment

### On Raspberry Pi

The PWA runs as-is when foswvs-go is deployed. No special setup needed:

```bash
# Deploy updates same as before
make deploy PI_HOST=pi@raspberrypi.local

# Users reinstall app to get latest manifest/icons:
# Android: Menu → Install app
# iOS: Share → Add to Home Screen
```

### HTTPS (Recommended but Optional)

PWAs work on HTTP, but HTTPS enables:
- Automatic install prompts
- Geolocation API
- Camera/microphone APIs
- Browser trust indicators

Generate self-signed cert for testing:

```bash
openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
  -keyout /home/pi/foswvs-go/ssl/foswvs.key \
  -out /home/pi/foswvs-go/ssl/foswvs.crt \
  -subj "/CN=10.0.0.1"

# Then enable TLS in foswvs-go (see INSTALL.md)
```

## Support

For issues or questions:

1. Check Chrome DevTools (Application tab)
2. Review console errors
3. See Troubleshooting section above
4. Open issue on [GitHub](https://github.com/foswvs/foswvs-go/issues)
