#!/usr/bin/env node
/**
 * Generate PWA icons from SVG template
 *
 * Usage:
 *   npm install sharp
 *   node scripts/generate-pwa-icons.js
 */

const fs = require('fs');
const path = require('path');

// Check if sharp is installed
let sharp;
try {
  sharp = require('sharp');
} catch (e) {
  console.error('Error: sharp module not found.');
  console.error('Install it with: npm install sharp');
  process.exit(1);
}

const ICON_DIR = path.join(__dirname, '..', 'web', 'static', 'icons');
const SIZES = [72, 96, 128, 144, 152, 192, 384, 512];

// Ensure icons directory exists
if (!fs.existsSync(ICON_DIR)) {
  fs.mkdirSync(ICON_DIR, { recursive: true });
  console.log(`Created directory: ${ICON_DIR}`);
}

// SVG icon template: a simple WiFi symbol in a teal/green circle
const SVG_TEMPLATE = `<svg viewBox="0 0 512 512" xmlns="http://www.w3.org/2000/svg">
  <!-- Background circle -->
  <circle cx="256" cy="256" r="256" fill="#0d9488"/>
  <!-- WiFi symbol -->
  <g transform="translate(256, 200)" fill="none" stroke="white" stroke-width="24" stroke-linecap="round" stroke-linejoin="round">
    <path d="M-80 0A120 120 0 0 1 80 0" />
    <path d="M-40 40A60 60 0 0 1 40 40" />
    <circle cx="0" cy="100" r="12" fill="white" stroke="none"/>
  </g>
</svg>`;

// SVG template for maskable icons (no background circle, just symbol)
const SVG_MASKABLE = `<svg viewBox="0 0 512 512" xmlns="http://www.w3.org/2000/svg">
  <!-- Maskable icon: just the WiFi symbol (will be masked by device) -->
  <g transform="translate(256, 200)" fill="none" stroke="#0d9488" stroke-width="24" stroke-linecap="round" stroke-linejoin="round">
    <path d="M-120 0A180 180 0 0 1 120 0" />
    <path d="M-60 60A100 100 0 0 1 60 60" />
    <circle cx="0" cy="150" r="18" fill="#0d9488" stroke="none"/>
  </g>
</svg>`;

async function generateIcon(size, isMaskable = false) {
  const svg = isMaskable ? SVG_MASKABLE : SVG_TEMPLATE;
  const suffix = isMaskable ? '-maskable' : '';
  const filename = `icon-${size}x${size}${suffix}.png`;
  const filepath = path.join(ICON_DIR, filename);

  try {
    await sharp(Buffer.from(svg))
      .resize(size, size, {
        fit: 'contain',
        background: { r: 255, g: 255, b: 255, alpha: 0 }
      })
      .png()
      .toFile(filepath);

    console.log(`✓ Generated: ${filename}`);
    return true;
  } catch (err) {
    console.error(`✗ Failed to generate ${filename}:`, err.message);
    return false;
  }
}

async function generateScreenshot(width, height, filename) {
  const svg = `<svg viewBox="0 0 512 512" xmlns="http://www.w3.org/2000/svg">
    <rect width="512" height="512" fill="#ffffff"/>
    <circle cx="256" cy="256" r="200" fill="#0d9488"/>
    <text x="256" y="280" font-size="80" font-weight="bold" text-anchor="middle" fill="white">
      PISO WIFI
    </text>
  </svg>`;

  const filepath = path.join(ICON_DIR, filename);

  try {
    await sharp(Buffer.from(svg))
      .resize(width, height, {
        fit: 'fill',
        background: { r: 255, g: 255, b: 255, alpha: 1 }
      })
      .png()
      .toFile(filepath);

    console.log(`✓ Generated: ${filename}`);
    return true;
  } catch (err) {
    console.error(`✗ Failed to generate ${filename}:`, err.message);
    return false;
  }
}

async function main() {
  console.log('Generating PWA icons...\n');

  let success = true;

  // Generate regular icons
  for (const size of SIZES) {
    const result = await generateIcon(size, false);
    success = success && result;
  }

  // Generate maskable icons (for adaptive icons on Android)
  console.log('\nGenerating maskable icons...');
  for (const size of [192, 512]) {
    const result = await generateIcon(size, true);
    success = success && result;
  }

  // Generate shortcut icon
  console.log('\nGenerating shortcut icon...');
  await generateIcon(96, false).then((result) => {
    if (result) {
      // Copy to shortcut name
      const src = path.join(ICON_DIR, 'icon-96x96.png');
      const dst = path.join(ICON_DIR, 'shortcut-96x96.png');
      try {
        fs.copyFileSync(src, dst);
        console.log(`✓ Generated: shortcut-96x96.png`);
      } catch (err) {
        console.error(`✗ Failed to create shortcut icon:`, err.message);
        success = false;
      }
    } else {
      success = false;
    }
  });

  // Generate screenshots
  console.log('\nGenerating screenshots...');
  await generateScreenshot(540, 720, 'screenshot-1.png');
  await generateScreenshot(1280, 720, 'screenshot-2.png');

  if (success) {
    console.log('\n✓ All icons generated successfully!');
    console.log(`Icons are located in: ${ICON_DIR}`);
    process.exit(0);
  } else {
    console.log('\n✗ Some icons failed to generate. Check errors above.');
    process.exit(1);
  }
}

main();
