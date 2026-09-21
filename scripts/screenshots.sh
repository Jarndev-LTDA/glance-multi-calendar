#!/usr/bin/env bash
# Renders the running service to PNG with headless Chrome: desktop and a
# 390 px phone frame, in both themes. Usage: ./scripts/screenshots.sh [out-dir] [base-url]
# Needs google-chrome and python3 with Pillow.
set -euo pipefail
OUT="${1:-/tmp/gmc-shots}"; BASE="${2:-http://localhost:8080}"
mkdir -p "$OUT"
# Chrome headless has a ~500 px minimum viewport, so the phone view is an
# iframe of 390 px inside a taller page.
for theme in dark light; do
  cat > "$OUT/_phone-$theme.html" <<HTML
<!doctype html><html><body style="margin:0;background:#000">
<iframe src="$BASE/?theme=$theme" style="width:390px;height:1000px;border:0;display:block"></iframe>
</body></html>
HTML
  google-chrome --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 \
    --screenshot="$OUT/raw-desktop-$theme.png" --window-size=1240,760 \
    --virtual-time-budget=2000 "$BASE/?theme=$theme" >/dev/null 2>&1
  google-chrome --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 \
    --screenshot="$OUT/raw-phone-$theme.png" --window-size=520,1040 \
    --virtual-time-budget=2000 "file://$OUT/_phone-$theme.html" >/dev/null 2>&1
done
python3 - "$OUT" <<'PY'
import sys
from PIL import Image, ImageChops
out = sys.argv[1]
for theme in ("dark", "light"):
    for kind in ("desktop", "phone"):
        im = Image.open(f"{out}/raw-{kind}-{theme}.png").convert("RGB")
        bg = im.getpixel((0, 0))
        box = ImageChops.difference(im, Image.new("RGB", im.size, bg)).getbbox() or (0, 0, im.width, im.height)
        l, t, r, b = box; p = 14
        im.crop((max(0, l-p), max(0, t-p), min(im.width, r+p), min(im.height, b+p))).save(f"{out}/{kind}-{theme}.png")
        print(f"{kind}-{theme}.png", Image.open(f"{out}/{kind}-{theme}.png").size)
PY
