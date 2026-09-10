#!/usr/bin/env python3
"""Read-only technical alpha checks for padded, isolated PNG/WebP sprites.

Requires Pillow. JSON stdout; exit 0 for technical pass, 1 for failed files,
2 for invalid arguments. A pass never approves visible art or material quality.
Region boxes use local decoded pixels and exclusive right/bottom coordinates.
"""
import argparse
import hashlib
import io
import json
import math
from pathlib import Path

from PIL import Image


def nonnegative_int(value):
    try:
        number = int(value)
        if number >= 0:
            return number
    except ValueError:
        pass
    raise argparse.ArgumentTypeError("must be a nonnegative integer")


def fraction(value):
    try:
        number = float(value)
        if math.isfinite(number) and 0 <= number <= 1:
            return number
    except ValueError:
        pass
    raise argparse.ArgumentTypeError("must be a finite fraction from 0 to 1")


def region(value):
    try:
        values = tuple(int(part) for part in value.split(","))
        if len(values) == 6:
            x0, y0, x1, y1, low, high = values
            if 0 <= x0 < x1 and 0 <= y0 < y1 and 0 <= low <= high <= 255:
                return values
    except ValueError:
        pass
    raise argparse.ArgumentTypeError(
        "use x0,y0,x1,y1,min_alpha,max_alpha; nonempty nonnegative box, "
        "exclusive x1/y1, and 0 <= min_alpha <= max_alpha <= 255"
    )


def alpha_stats(histogram):
    count = sum(histogram)
    occupied = [value for value, total in enumerate(histogram) if total]
    clear, opaque = histogram[0], histogram[255]
    partial = count - clear - opaque
    mean = sum(value * total for value, total in enumerate(histogram)) / count if count else None
    return {
        "pixels": count,
        "min": min(occupied) if occupied else None,
        "max": max(occupied) if occupied else None,
        "clear_pixels": clear,
        "partial_pixels": partial,
        "opaque_pixels": opaque,
        "clear_fraction": clear / count if count else None,
        "partial_fraction": partial / count if count else None,
        "opaque_fraction": opaque / count if count else None,
        "mean": mean,
        "mean_background_weight_fraction": 1 - mean / 255 if mean is not None else None,
    }


def inspect_image(path, border=1, min_clear_fraction=0.01, regions=()):
    """Inspect one file without writing or changing its pixels."""
    path = Path(path)
    errors = []
    result = {"path": str(path.absolute()), "errors": errors,
              "technical_pass": False, "visual_review_required": True}
    try:
        payload = path.read_bytes()
        result["sha256"] = hashlib.sha256(payload).hexdigest()
        result["bytes"] = len(payload)
        with Image.open(io.BytesIO(payload)) as image:
            result.update(format=image.format, mode=image.mode,
                          size=list(image.size), frames=getattr(image, "n_frames", 1),
                          explicit_alpha=("A" in image.getbands() or "transparency" in image.info))
            if image.format not in {"PNG", "WEBP"}:
                errors.append("Transparent deliverables must decode as PNG or WebP.")
            if result["frames"] != 1:
                errors.append("Only single-frame sprites are supported; remaining frames are not audited.")
            image.load()
            alpha = image.convert("RGBA").getchannel("A")
        histogram = alpha.histogram()
        stats = alpha_stats(histogram)
        result["alpha"] = stats
        bounds = alpha.getbbox()
        result["visible_bounds"] = list(bounds) if bounds else None
        result["effective_transparency"] = stats["min"] < 255
        if not result["effective_transparency"]:
            errors.append("No effective transparency: all decoded pixels are opaque.")
        if not stats["clear_pixels"]:
            errors.append("No fully clear pixels (alpha 0).")
        if stats["max"] == 0:
            errors.append("Empty image: no visible pixels.")
        if stats["clear_fraction"] < min_clear_fraction:
            errors.append(f"Clear fraction {stats['clear_fraction']:.6f} is below {min_clear_fraction:g}.")

        width, height = alpha.size
        border_histogram = histogram.copy() if border else [0] * 256
        if border and width > 2 * border and height > 2 * border:
            inside = alpha.crop((border, border, width - border, height - border)).histogram()
            border_histogram = [outer - inner for outer, inner in zip(histogram, inside)]
        result["border"] = {"width": border, **alpha_stats(border_histogram)}
        if border and result["border"]["max"] != 0:
            errors.append(f"The full {border}-pixel border must be clear (alpha 0).")

        result["regions"] = []
        for values in regions:
            x0, y0, x1, y1, low, high = values
            check = {"box": [x0, y0, x1, y1], "expected_alpha": [low, high]}
            result["regions"].append(check)
            if not (0 <= x0 < x1 <= width and 0 <= y0 < y1 <= height and 0 <= low <= high <= 255):
                check["technical_pass"] = False
                errors.append(f"Invalid region {values}: box must fit decoded {width}x{height} image.")
                continue
            roi = alpha.crop((x0, y0, x1, y1)).histogram()
            mismatch = sum(roi[:low]) + sum(roi[high + 1:])
            check.update(alpha=alpha_stats(roi), mismatched_pixels=mismatch, technical_pass=mismatch == 0)
            if mismatch:
                errors.append(f"Region {check['box']}: {mismatch} pixels outside alpha [{low}, {high}].")
    except (OSError, ValueError, Image.DecompressionBombError) as error:
        errors.append(f"Could not decode image: {error}")
    result["technical_pass"] = not errors
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("files", nargs="+", type=Path)
    parser.add_argument("--border", type=nonnegative_int, default=1,
                        help="full clear border width (default 1); 0 for intentionally clipped registered sources")
    parser.add_argument("--min-clear-fraction", type=fraction, default=0.01,
                        help="minimum alpha-zero fraction (default 0.01, for padded isolated sprite briefs)")
    parser.add_argument("--region", action="append", type=region, default=[],
                        metavar="X0,Y0,X1,Y1,MIN,MAX",
                        help="require ALL local pixels in this box to lie in inclusive alpha range; repeatable, applies to each file")
    args = parser.parse_args(argv)
    results = [inspect_image(path, args.border, args.min_clear_fraction, args.region) for path in args.files]
    passed = all(result["technical_pass"] for result in results)
    report = {
        "schema_version": 1, "technical_pass": passed, "visual_review_required": True,
        "scope": "Technical checks for padded isolated sprites only. No checker-pattern detection, material approval, or repairs.",
        "policy": {"border": args.border, "min_clear_fraction": args.min_clear_fraction,
                   "regions": [list(value) for value in args.region]},
        "results": results,
    }
    print(json.dumps(report, indent=2))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
