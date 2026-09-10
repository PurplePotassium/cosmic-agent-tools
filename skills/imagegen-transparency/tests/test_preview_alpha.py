"""Verify diagnostic composition, source preservation, and collision behavior."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from PIL import Image


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "preview_alpha.py"


class PreviewAlphaTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def run_cli(self, source, output):
        return subprocess.run([sys.executable, "-B", str(SCRIPT), str(source), "--output", str(output)],
                              cwd=self.root, capture_output=True, text=True, check=False)

    def source(self):
        source = self.root / "source.png"
        image = Image.new("RGBA", (8, 6), (255, 0, 255, 0))
        image.putpixel((2, 2), (255, 0, 0, 128))
        image.putpixel((3, 3), (10, 20, 30, 255))
        image.save(source)
        return source

    def test_native_composites_preserve_alpha_and_ignore_hidden_rgb(self):
        source, output = self.source(), self.root / "review"
        before = source.read_bytes()
        completed = self.run_cli(source, output)
        self.assertEqual(completed.returncode, 0, completed.stderr + completed.stdout)
        report = json.loads(completed.stdout)
        self.assertEqual(source.read_bytes(), before)
        self.assertEqual(report["source_sha256"], hashlib.sha256(before).hexdigest())
        self.assertTrue(report["visual_review_required"])
        self.assertFalse(report["generated_artwork_modified"])
        with Image.open(output / "on-dark.png") as image:
            self.assertEqual(image.size, (8, 6))
            self.assertEqual(image.getpixel((0, 0)), (28, 31, 38))
            self.assertEqual(image.getpixel((3, 3)), (10, 20, 30))
            self.assertEqual(image.getpixel((2, 2)), (142, 15, 19))
        with Image.open(output / "on-light.png") as image:
            self.assertEqual(image.getpixel((0, 0)), (246, 246, 240))
            self.assertEqual(image.getpixel((2, 2)), (251, 123, 120))
        with Image.open(output / "alpha.png") as alpha, Image.open(source) as raw:
            self.assertEqual(alpha.tobytes(), raw.getchannel("A").tobytes())
        for key, path in report["outputs"].items():
            self.assertEqual(report["output_sha256"][key], hashlib.sha256(Path(path).read_bytes()).hexdigest())

    def test_existing_output_directory_and_source_are_never_overwritten(self):
        source = self.source()
        before = source.read_bytes()
        output = self.root / "review"
        output.mkdir()
        marker = output / "keep.txt"
        marker.write_text("preserve", encoding="utf-8")
        self.assertEqual(self.run_cli(source, output).returncode, 1)
        self.assertEqual(marker.read_text(encoding="utf-8"), "preserve")
        self.assertEqual(list(output.iterdir()), [marker])
        self.assertEqual(self.run_cli(source, source).returncode, 1)
        self.assertEqual(source.read_bytes(), before)

    def test_rgb_input_stays_visibly_opaque_in_diagnostics(self):
        source, output = self.root / "opaque.png", self.root / "review"
        Image.new("RGB", (8, 6), (110, 120, 130)).save(source)
        self.assertEqual(self.run_cli(source, output).returncode, 0)
        with Image.open(output / "on-dark.png") as dark, Image.open(output / "on-light.png") as light:
            self.assertEqual(dark.tobytes(), light.tobytes())
        with Image.open(output / "alpha.png") as alpha:
            self.assertEqual(alpha.getextrema(), (255, 255))

    def test_corrupt_missing_and_multiframe_inputs_fail_without_output(self):
        broken = self.root / "broken.png"
        broken.write_bytes(b"not an image")
        animated = self.root / "animated.gif"
        Image.new("RGB", (8, 6), "red").save(animated, save_all=True,
            append_images=[Image.new("RGB", (8, 6), "blue")], duration=100, loop=0)
        for index, source in enumerate((broken, self.root / "missing.png", animated)):
            with self.subTest(source=source.name):
                output = self.root / str(index)
                completed = self.run_cli(source, output)
                self.assertEqual(completed.returncode, 1)
                self.assertIn("error", json.loads(completed.stdout))
                self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
