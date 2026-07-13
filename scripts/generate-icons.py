#!/usr/bin/env python3
"""
Generate PWA icons in PNG format

Requirements:
  pip install Pillow

Usage:
  python3 scripts/generate-icons.py
"""

import os
import sys
from pathlib import Path
from io import BytesIO

try:
    from PIL import Image, ImageDraw
except ImportError:
    print("Error: Pillow is required but not installed.")
    print("Install it with: pip install Pillow")
    sys.exit(1)

# Icon sizes to generate
ICON_SIZES = [72, 96, 128, 144, 152, 192, 384, 512]
IOS_SIZES = [120, 152, 167, 180]  # iOS specific sizes
MASKABLE_SIZES = [192, 512]

# Colors
BRAND_COLOR = "#0d9488"  # Teal/green
WHITE = "#ffffff"

ICON_DIR = Path(__file__).parent.parent / "web" / "static" / "icons"


def hex_to_rgb(hex_color):
    """Convert hex color to RGB tuple"""
    hex_color = hex_color.lstrip('#')
    return tuple(int(hex_color[i:i+2], 16) for i in (0, 2, 4))


def create_base_icon(size, include_background=True):
    """Create a base icon with WiFi symbol"""
    # Create image with transparent background
    img = Image.new('RGBA', (size, size), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)

    brand_rgb = hex_to_rgb(BRAND_COLOR)
    white_rgb = hex_to_rgb(WHITE)

    if include_background:
        # Draw background circle
        margin = size * 0.02
        draw.ellipse(
            [margin, margin, size - margin, size - margin],
            fill=brand_rgb
        )
        symbol_color = white_rgb
    else:
        symbol_color = brand_rgb

    # Draw WiFi symbol centered in the icon
    center_x = size / 2
    center_y = size / 2.5  # Slightly higher than center

    stroke_width = max(1, int(size / 20))

    # Scale WiFi arcs based on icon size
    scale = size / 200

    # Outer arc
    arc1_radius = 40 * scale
    arc1_bbox = [
        center_x - arc1_radius,
        center_y - arc1_radius,
        center_x + arc1_radius,
        center_y + arc1_radius,
    ]
    draw.arc(arc1_bbox, 0, 180, fill=symbol_color, width=stroke_width)

    # Middle arc
    arc2_radius = 20 * scale
    arc2_bbox = [
        center_x - arc2_radius,
        center_y - arc2_radius,
        center_x + arc2_radius,
        center_y + arc2_radius,
    ]
    draw.arc(arc2_bbox, 0, 180, fill=symbol_color, width=stroke_width)

    # Signal dot
    dot_radius = 5 * scale
    dot_y = center_y + 50 * scale
    draw.ellipse(
        [
            center_x - dot_radius,
            dot_y - dot_radius,
            center_x + dot_radius,
            dot_y + dot_radius,
        ],
        fill=symbol_color
    )

    return img


def create_maskable_icon(size):
    """Create a maskable icon for Android adaptive icons"""
    return create_base_icon(size, include_background=False)


def save_icon(img, filename):
    """Save icon as PNG"""
    filepath = ICON_DIR / filename
    # Convert RGBA to RGB for better compatibility (PNG will still support transparency)
    if img.mode == 'RGBA':
        # Keep RGBA for transparent backgrounds
        img.save(filepath, 'PNG')
    else:
        img.save(filepath, 'PNG')
    print(f"✓ Generated: {filename}")
    return True


def generate_regular_icons():
    """Generate regular icons for all platforms"""
    print("Generating regular icons...\n")
    success = True

    for size in ICON_SIZES:
        img = create_base_icon(size, include_background=True)
        filename = f"icon-{size}x{size}.png"
        try:
            save_icon(img, filename)
        except Exception as e:
            print(f"✗ Failed to generate {filename}: {e}")
            success = False

    return success


def generate_ios_icons():
    """Generate iOS-specific icon sizes"""
    print("\nGenerating iOS icons...\n")
    success = True

    for size in IOS_SIZES:
        img = create_base_icon(size, include_background=True)
        filename = f"icon-{size}x{size}.png"
        try:
            save_icon(img, filename)
        except Exception as e:
            print(f"✗ Failed to generate {filename}: {e}")
            success = False

    return success


def generate_maskable_icons():
    """Generate maskable icons for Android adaptive icons"""
    print("\nGenerating maskable icons...\n")
    success = True

    for size in MASKABLE_SIZES:
        img = create_maskable_icon(size)
        filename = f"icon-{size}x{size}-maskable.png"
        try:
            save_icon(img, filename)
        except Exception as e:
            print(f"✗ Failed to generate {filename}: {e}")
            success = False

    return success


def generate_shortcut_icon():
    """Generate shortcut icon"""
    print("\nGenerating shortcut icon...\n")
    try:
        img = create_base_icon(96, include_background=True)
        save_icon(img, "shortcut-96x96.png")
        return True
    except Exception as e:
        print(f"✗ Failed to generate shortcut icon: {e}")
        return False


def generate_screenshots():
    """Generate placeholder screenshots"""
    print("\nGenerating screenshots...\n")
    success = True

    # Screenshot 1: 540x720 (narrow/mobile)
    try:
        img = Image.new('RGB', (540, 720), hex_to_rgb(WHITE))
        draw = ImageDraw.Draw(img)
        brand_rgb = hex_to_rgb(BRAND_COLOR)

        # Draw circle background
        margin = 60
        draw.ellipse([margin, 150, 540-margin, 570], fill=brand_rgb)

        # Draw text
        try:
            # Try to use a larger font if available
            draw.text((270, 350), "PISO WIFI", fill=WHITE, anchor="mm")
        except:
            draw.text((270, 350), "PISO WIFI", fill=WHITE)

        save_icon(img, "screenshot-1.png")
    except Exception as e:
        print(f"✗ Failed to generate screenshot-1.png: {e}")
        success = False

    # Screenshot 2: 1280x720 (wide/desktop)
    try:
        img = Image.new('RGB', (1280, 720), hex_to_rgb(WHITE))
        draw = ImageDraw.Draw(img)
        brand_rgb = hex_to_rgb(BRAND_COLOR)

        # Draw circle background
        margin = 100
        draw.ellipse([margin, 50, 400, 670], fill=brand_rgb)

        # Draw text
        try:
            draw.text((640, 360), "PISO WIFI", fill=brand_rgb, anchor="mm")
        except:
            draw.text((640, 360), "PISO WIFI", fill=brand_rgb)

        save_icon(img, "screenshot-2.png")
    except Exception as e:
        print(f"✗ Failed to generate screenshot-2.png: {e}")
        success = False

    return success


def main():
    """Main function"""
    print("PWA Icon Generator for foswvs-go\n")
    print(f"Icons directory: {ICON_DIR}\n")

    # Create icons directory
    ICON_DIR.mkdir(parents=True, exist_ok=True)

    success = True
    success &= generate_regular_icons()
    success &= generate_ios_icons()
    success &= generate_maskable_icons()
    success &= generate_shortcut_icon()
    success &= generate_screenshots()

    if success:
        print("\n✓ All icons generated successfully!")
        print(f"Icons are located in: {ICON_DIR}")

        # Show generated files
        files = sorted(ICON_DIR.glob("*.png"))
        print(f"\nGenerated {len(files)} image files:")
        for f in files:
            size_mb = f.stat().st_size / 1024
            print(f"  • {f.name} ({size_mb:.1f} KB)")

        return 0
    else:
        print("\n✗ Some icons failed to generate. Check errors above.")
        return 1


if __name__ == "__main__":
    sys.exit(main())
