#!/usr/bin/env python3
"""Create alpha diagnostics in a new directory without changing the source.

Requires Pillow. Native light/dark/stripe composites and alpha; reduced sheet.
Exit 0 means diagnostics were created, never asset acceptance. Exit 1 means a
file/output error; argparse uses exit 2 for invalid command-line arguments.
"""
import argparse
import hashlib
import io
import json
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont, ImageOps


BACKGROUNDS = {"light": (246, 246, 240), "dark": (28, 31, 38)}


def backdrop(kind, size):
    if kind in BACKGROUNDS:
        return Image.new("RGBA", size, (*BACKGROUNDS[kind], 255))
    image = Image.new("RGBA", size, (240, 211, 88, 255))
    draw = ImageDraw.Draw(image)
    stripe = max(1, round(min(size) / 18))
    for x in range(0, size[0], stripe * 2):
        draw.rectangle((x, 0, x + stripe - 1, size[1] - 1), fill=(49, 124, 153, 255))
    return image


def build_diagnostics(source, output):
    source, output = Path(source).resolve(), Path(output).absolute()
    payload = source.read_bytes()
    with Image.open(io.BytesIO(payload)) as image:
        frames = getattr(image, "n_frames", 1)
        if frames != 1:
            raise ValueError("Animated inputs require a separate per-frame review; no output created.")
        image.load()
        input_format, input_mode = image.format, image.mode
        rgba = image.convert("RGBA")

    # A fresh directory makes output collisions explicit and preserves earlier reviews.
    output.mkdir(parents=True, exist_ok=False)
    rendered = {}
    for kind in ("light", "dark", "stripes"):
        rendered[kind] = Image.alpha_composite(backdrop(kind, rgba.size), rgba).convert("RGB")
    rendered["alpha"] = rgba.getchannel("A")

    paths = {}
    hashes = {}
    for kind, image in rendered.items():
        path = output / ("alpha.png" if kind == "alpha" else f"on-{kind}.png")
        image.save(path, format="PNG")
        paths[kind] = str(path)
        hashes[kind] = hashlib.sha256(path.read_bytes()).hexdigest()

    cell, gap, header = 256, 12, 90
    sheet = Image.new("RGB", (4 * cell + 5 * gap, header + cell + gap), "#e8e8e8")
    draw = ImageDraw.Draw(sheet)
    title_font = ImageFont.load_default(size=22)
    caption_font = ImageFont.load_default(size=15)
    draw.text((gap, 10), "Alpha diagnostics - unchanged source", fill="#171717", font=title_font)
    draw.text((gap, 39), "Open native views for edges and materials. This sheet does not certify acceptance.",
              fill="#424242", font=caption_font)
    captions = ("LIGHT", "DARK", "SATURATED STRIPES", "ALPHA: WHITE = OPAQUE")
    for index, ((kind, image), caption) in enumerate(zip(rendered.items(), captions)):
        x = gap + index * (cell + gap)
        draw.text((x, 66), caption, fill="#171717", font=caption_font)
        thumbnail = ImageOps.contain(image.convert("RGB"), (cell, cell), Image.Resampling.LANCZOS)
        sheet.paste(thumbnail, (x + (cell - thumbnail.width) // 2,
                               header + (cell - thumbnail.height) // 2))
    sheet_path = output / "contact-sheet.png"
    sheet.save(sheet_path, format="PNG")
    paths["contact_sheet"] = str(sheet_path)
    hashes["contact_sheet"] = hashlib.sha256(sheet_path.read_bytes()).hexdigest()
    report = {
        "source": str(source), "source_sha256": hashlib.sha256(payload).hexdigest(),
        "source_format": input_format, "source_mode": input_mode, "source_size": list(rgba.size),
        "generated_artwork_modified": False, "visual_review_required": True,
        "scope": "Diagnostic source-over composites and alpha only; no repairs or acceptance decision.",
        "native_outputs": ["light", "dark", "stripes", "alpha"],
        "outputs": paths, "output_sha256": hashes,
        "backgrounds": {**{key: list(value) for key, value in BACKGROUNDS.items()},
                        "stripes": [[49, 124, 153], [240, 211, 88]]},
    }
    (output / "diagnostics.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("--output", type=Path, required=True, help="new directory; must not already exist")
    args = parser.parse_args(argv)
    try:
        report = build_diagnostics(args.source, args.output)
    except (OSError, ValueError, Image.DecompressionBombError) as error:
        print(json.dumps({"error": str(error), "visual_review_required": True}))
        return 1
    print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
