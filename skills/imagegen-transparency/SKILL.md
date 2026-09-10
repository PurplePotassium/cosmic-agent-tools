---
name: imagegen-transparency
description: "Generate, inspect, and troubleshoot raster assets requiring genuine transparent backgrounds or translucent materials. Use for transparent PNG/WebP cutouts, sprites, glass, liquids, nets, hair, eyewear, glow, sheer cloth, or stained glass; catch painted checkerboards, opaque backdrops, filled holes, and nearly opaque materials before acceptance."
---

# Imagegen Transparency

Produce assets that composite correctly, with evidence from decoded pixels and
actual background changes. This skill is portable: all resources are relative
to this folder. No project files, knowledge store, credentials, or network calls
are required by its inspection scripts. Python and Pillow are required.

## Generation and acceptance

1. Identify which parts are **solid**, **empty holes**, **translucent material**,
   and **emitted light**. Follow the requested art style and current approved
   sources. Read the relevant row in [materials.md](references/materials.md)
   when glass, contents, fine structures, fabric or glow are involved.
2. Use the host's built-in image generator when available, following its image
   instructions and the user's authorized route. If an authorized tool exposes
   explicit transparency/output-format options, use the documented options for
   the selected model. Never invent arguments or claim an unexposed option was
   set. Provider/model/key changes require existing task authorization.
3. Preserve the exact returned file before conversion, plus the prompt,
   references and exposed tool metadata. Ask for native alpha with the prompt
   below. Reference clean source art, not a screenshot of a checker preview.
   These words express the contract; they do not guarantee the output.
4. Run the bundled checker on the **raw return**, then inspect the native
   composites and alpha view. Select relevant interior regions to check actual
   holes and meaningful material opacity. Whole-image partial-alpha counts,
   transparent corners, a PNG extension and RGBA mode are insufficient.
5. After any authorized editing, matting, resizing, atlas packing or export,
   repeat the checks on the **actual file that will be consumed**, at native and
   intended display size. Review in the target compositor when available.
   Preserve fractional edges and material alpha; avoid RGB/JPEG flattening.
6. Report exterior, holes/materials and visual results separately. Do not accept
   a transparency-required asset until its applicable numeric expectations and
   full visual review pass. Keep unresolved drafts distinct from accepted art.

An audit request does not authorize regenerating or repairing the art. The
bundled tools inspect source pixels and create diagnostic derivatives only.

## Prompt contract

> Create one isolated [asset] in [requested style] for compositing. Deliver a
> PNG with genuine alpha transparency and generous clear margins. Outside the
> asset and within [named empty openings], alpha must be zero. [Named solid
> parts] remain opaque. [Named translucent parts] use smooth partial alpha so
> the eventual background is visibly revealed; retain highlights and fine
> edges. [If requested: glow fades smoothly to alpha zero.] No painted
> checkerboard, transparency-preview pattern, solid backdrop, floor, cast
> shadow, surrounding scene, labels or interface.

Tailor this to the brief: do not force opaque liquid to become transparent or
remove a requested shadow. For intentionally clipped or full-bleed layers,
define an appropriate edge contract instead of demanding padded margins.

## Run the tools

Resolve script paths against this skill directory, regardless of the working
directory. Examples below assume this folder is the working directory:

```text
python scripts/check_alpha.py /path/to/asset.png
python scripts/preview_alpha.py /path/to/asset.png --output /path/to/new-review-directory
python scripts/check_alpha.py /path/to/asset.png --region 100,100,120,120,0,0
```

The checker emits JSON and hashes without changing the source. Default policy
is one isolated visible PNG/WebP sprite, at least 1% fully clear pixels, and a
fully clear one-pixel border. Exit 0 means technical pass, 1 means a failed file
or expectation, and 2 means invalid CLI arguments. Multiple input files are
supported. Animated files are rejected because only one frame is inspected.
The checker always requires at least one fully clear and one visible pixel;
full-bleed translucent layers with no clear pixels need a different documented
validator, even if `--border 0` and `--min-clear-fraction 0` are specified.

`--region x0,y0,x1,y1,min_alpha,max_alpha` checks **every pixel** in a native
rectangle; right/bottom are exclusive and alpha bounds inclusive. Repeat it for
multiple holes or material patches. The example tests a clear hole; coordinates
must be chosen from the actual asset. For translucent regions, choose bounds
from the intended appearance, including reflections where appropriate. There
is no universal correct glass or liquid opacity. Options apply to each input
file, so audit differing layouts separately.

The preview tool writes native light, dark, saturated-striped and alpha PNGs,
a reduced contact sheet, and source/output hashes into a **new** directory.
It never overwrites an existing directory or source. Open the rendered files;
successful execution is not visual review. Its exit 0 means diagnostics were
created, not that the asset passed. Inspect the whole contour and all internal
holes/materials, then inspect intended-size and actual-use composites too.

If the intended size or target renderer is unavailable, conclude the native-file
audit and label that display/integration validation as unperformed. A clearly
labelled representative resize can supplement the audit; do not present it as
the actual game size or block a file-review result on unspecified integration.

## Interpret the evidence

- **Partial alpha can be nearly opaque.** Values 251–254 are approximately
  98–100% opaque. `alpha.mean_background_weight_fraction` reports mean
  `1 - alpha/255`, the ordinary source-over background contribution, not physical
  optical transmittance. Apply it to meaningful regions, not the whole canvas.
- Alpha-1 edge dust is about 0.39% opacity. It fails a strict clear-border or
  clear-hole contract, but it is different from a solid backdrop. Do not disable
  the contract merely to get PASS. `--border 0` is for a documented clipped-layer
  brief; `--min-clear-fraction` adjusts the policy for a different composition.
- Legitimate fractional-only art need not contain any alpha-255 pixels. Do not
  hard-threshold hair, glass, glow or fabric to binary alpha.
- An ordinary request for "opaque" frames, corks or candy describes visual
  intent. Require solid cores to equal exactly alpha 255 only when an explicit
  numeric or pipeline contract demands it. Otherwise record nearly opaque
  values and judge their appearance; do not invent a 255-only region gate.
- RGB beneath alpha zero is invisible when correctly composited. A raw preview
  can expose misleading colors; actual composites decide what is visible.
- The checker does **not** detect a painted checker pattern, prove correct
  topology, judge style, or certify glass optics. Even technically passing
  interiors can contain baked patterns or an unsuitable material treatment.

For a failed result, read [diagnosis.md](references/diagnosis.md). Use focused
retries only when generation/editing is authorized. After three focused failed
attempts, report the remaining defect and propose a different construction;
do not silently switch providers or keep editing contaminated glass.

## Share and maintain

Copy this whole folder, including scripts and references. See
[INSTALL.md](INSTALL.md) for installation and dependencies. Keep automatic skill
selection enabled. In a consuming agent's instructions, a useful trigger is:
"Before generating or accepting raster art requiring transparency, use the
imagegen-transparency skill and verify raw/final alpha plus material behavior."

Run `python -B -m unittest discover -s tests -v` after script changes. Tests use
temporary synthetic images; no generator, credentials or private assets are
needed. Measured limitations motivating this skill are summarized in
[evidence.md](references/evidence.md).
