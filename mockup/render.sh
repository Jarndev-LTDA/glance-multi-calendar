#!/usr/bin/env bash
# Renderiza o mockup em PNG (desktop + celular real de 390px via iframe).
# Chrome headless tem largura minima de ~500px de viewport: por isso o celular
# e renderizado dentro de um iframe de 390px (_phone.html), nao via --window-size.
set -euo pipefail
cd "$(dirname "$0")"
OUT="${1:-/tmp}"
google-chrome --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 \
  --screenshot="$OUT/raw-desktop.png" --window-size=1240,720 \
  --virtual-time-budget=1500 "file://$PWD/agenda.html" >/dev/null 2>&1
google-chrome --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 \
  --screenshot="$OUT/raw-mobile.png" --window-size=520,1040 \
  --virtual-time-budget=1500 "file://$PWD/_phone.html" >/dev/null 2>&1
python3 - "$OUT" <<'PY'
import sys
from PIL import Image, ImageChops
out = sys.argv[1]
for src, dst in (("raw-desktop.png","agenda-desktop.png"), ("raw-mobile.png","agenda-mobile.png")):
    im = Image.open(f"{out}/{src}").convert("RGB")
    l,t,r,b = ImageChops.difference(im, Image.new("RGB", im.size, (27,28,34))).getbbox()
    p = 14
    im.crop((max(0,l-p), max(0,t-p), min(im.width,r+p), min(im.height,b+p))).save(f"{out}/{dst}")
    print(dst, Image.open(f"{out}/{dst}").size)
PY
