"""Behavior checks using synthetic images in a temporary directory only."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from PIL import Image


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "check_alpha.py"


class CheckAlphaTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def save(self, image, name="sprite.png", **options):
        path = self.root / name
        image.save(path, **options)
        return path

    def sprite(self, alpha=254):
        image = Image.new("RGBA", (20, 20))
        image.paste((180, 90, 40, alpha), (4, 4, 16, 16))
        return image

    def run_cli(self, *args):
        return subprocess.run([sys.executable, "-B", str(SCRIPT), *map(str, args)],
                              capture_output=True, text=True, check=False)

    def audit(self, path, expected, *args):
        before = path.read_bytes()
        completed = self.run_cli(path, *args)
        self.assertEqual(completed.returncode, expected, completed.stderr + completed.stdout)
        report = json.loads(completed.stdout)
        self.assertTrue(report["visual_review_required"])
        self.assertEqual(report["technical_pass"], expected == 0)
        self.assertEqual(path.read_bytes(), before, "Validator must not change input bytes")
        result = report["results"][0]
        self.assertEqual(result["sha256"], hashlib.sha256(before).hexdigest())
        return result

    def test_rgb_painted_checker_fails(self):
        image = Image.new("RGB", (20, 20))
        image.putdata([(220, 220, 220) if (x // 2 + y // 2) % 2 else (255, 255, 255)
                       for y in range(20) for x in range(20)])
        result = self.audit(self.save(image), 1)
        self.assertFalse(result["explicit_alpha"])
        self.assertEqual(result["alpha"]["opaque_pixels"], 400)

    def test_rgba_all_opaque_fails(self):
        result = self.audit(self.save(Image.new("RGBA", (20, 20), (30, 60, 90, 255))), 1)
        self.assertTrue(result["explicit_alpha"])
        self.assertFalse(result["effective_transparency"])

    def test_fractional_only_art_passes_and_reports_full_border(self):
        result = self.audit(self.save(self.sprite()), 0)
        self.assertEqual(result["alpha"]["max"], 254)
        self.assertEqual(result["alpha"]["opaque_pixels"], 0)
        self.assertEqual(result["alpha"]["partial_pixels"], 144)
        self.assertEqual(result["alpha"]["clear_pixels"], 256)
        self.assertAlmostEqual(result["alpha"]["clear_fraction"], 0.64)
        self.assertEqual(result["border"]["pixels"], 76)
        self.assertEqual(result["visible_bounds"], [4, 4, 16, 16])

    def test_palette_transparency_passes(self):
        image = Image.new("P", (20, 20), 0)
        image.putpalette([0, 0, 0, 180, 90, 40] + [0] * 762)
        image.paste(1, (4, 4, 16, 16))
        result = self.audit(self.save(image, transparency=0), 0)
        self.assertEqual(result["mode"], "P")
        self.assertTrue(result["explicit_alpha"])
        self.assertEqual(result["alpha"]["clear_pixels"], 256)

    def test_empty_fails(self):
        result = self.audit(self.save(Image.new("RGBA", (20, 20))), 1)
        self.assertIsNone(result["visible_bounds"])
        self.assertIn("Empty image: no visible pixels.", result["errors"])

    def test_tiny_clear_fraction_fails_with_clipped_border_disabled(self):
        image = Image.new("RGBA", (20, 20), (60, 60, 60, 254))
        image.putpixel((0, 0), (0, 0, 0, 0))
        path = self.save(image)
        self.audit(path, 1, "--border", 0)
        self.audit(path, 0, "--border", 0, "--min-clear-fraction", 0.001)

    def test_clear_border_does_not_certify_cavity(self):
        path = self.save(self.sprite(255))
        self.audit(path, 0)
        result = self.audit(path, 1, "--region", "7,7,13,13,0,0")
        self.assertEqual(result["regions"][0]["mismatched_pixels"], 36)

    def test_repeatable_region_checks_every_pixel_and_inclusive_range(self):
        image = self.sprite()
        path = self.save(image)
        self.audit(path, 0, "--region", "0,0,3,3,0,0", "--region", "5,5,15,15,200,254")
        image.putpixel((14, 14), (80, 80, 80, 199))
        result = self.audit(self.save(image), 1, "--region", "5,5,15,15,200,254")
        self.assertEqual(result["regions"][0]["mismatched_pixels"], 1)

    def test_invalid_region_bounds_fail(self):
        result = self.audit(self.save(self.sprite()), 1, "--region", "0,0,21,20,0,255")
        self.assertFalse(result["regions"][0]["technical_pass"])

    def test_partial_alpha_can_have_almost_no_background_contribution(self):
        path = self.save(self.sprite(252))
        result = self.audit(path, 0, "--region", "5,5,15,15,0,255")
        stats = result["regions"][0]["alpha"]
        self.assertEqual(stats["partial_fraction"], 1)
        self.assertEqual(stats["mean"], 252)
        self.assertAlmostEqual(stats["mean_background_weight_fraction"], 3 / 255)
        self.audit(path, 1, "--region", "5,5,15,15,0,180")

    def test_invalid_arguments_fail_without_json(self):
        path = self.save(self.sprite())
        for option, value in [("--region", "1,1,1,5,0,0"), ("--region", "0,0,3,3,255,0"),
                              ("--region", "x,0,3,3,0,0"), ("--region", "0,0,3,3,0,256"),
                              ("--border", "-1"), ("--min-clear-fraction", "nan"),
                              ("--min-clear-fraction", "1.1")]:
            with self.subTest(option=option, value=value):
                result = self.run_cli(path, option, value)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(result.stdout, "")

    def test_nonclear_border_fails_and_can_be_disabled(self):
        image = self.sprite()
        image.putpixel((0, 10), (80, 80, 80, 1))
        path = self.save(image)
        result = self.audit(path, 1)
        self.assertEqual(result["border"]["partial_pixels"], 1)
        result = self.audit(path, 0, "--border", 0)
        self.assertEqual(result["border"]["pixels"], 0)

    def test_wider_border_includes_pixels_once(self):
        image = self.sprite()
        image.putpixel((1, 10), (80, 80, 80, 1))
        path = self.save(image)
        self.audit(path, 0)
        result = self.audit(path, 1, "--border", 2)
        self.assertEqual(result["border"]["pixels"], 144)

    def test_webp_passes_and_gif_fails_actual_format_check(self):
        self.audit(self.save(self.sprite(), "sprite.webp", lossless=True), 0)
        result = self.audit(self.save(self.sprite(), "disguised.png", format="GIF"), 1)
        self.assertEqual(result["format"], "GIF")

    def test_multiple_files_missing_and_corrupt_files_fail_cleanly(self):
        good = self.save(self.sprite())
        bad = self.root / "corrupt.png"
        bad.write_bytes(b"not an image")
        missing = self.root / "missing.png"
        completed = self.run_cli(good, bad, missing)
        self.assertEqual(completed.returncode, 1)
        results = json.loads(completed.stdout)["results"]
        self.assertEqual([result["technical_pass"] for result in results], [True, False, False])


if __name__ == "__main__":
    unittest.main()
