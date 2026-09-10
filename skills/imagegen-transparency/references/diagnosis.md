# Diagnose transparent-asset failures

| Observation | Interpretation | Next action within the authorized task |
| --- | --- | --- |
| RGB, or RGBA whose alpha is entirely 255 | File is opaque regardless of preview/extension. | Reject as a transparent deliverable. Compare the raw return with later exports to locate where opacity arose. |
| Clear exterior but checks or a color plate inside glass/holes | A real alpha channel does not remove baked interior contamination. | Inspect complete interiors. Restart from clean references or use a deliberate authorized material/matte construction. |
| Many partial-alpha pixels but no visible background through material | Partial values near 255 can be essentially opaque. | Measure native material regions and review stripes/light/dark. Choose opacity appropriate to the brief. |
| Good raw file, opaque exported file | Conversion, flattening or an export/import stage is implicated. | Check the first stage where decoded alpha changed; retain a PNG/WebP alpha-preserving path. |
| Odd background color in raw preview, clean changing composites | Hidden RGB or preview handling may explain the appearance. | Judge correct alpha composition; do not repair invisible RGB without a demonstrated problem. |
| Faint alpha-1 pixels on an otherwise empty border/hole | About 0.39% residual opacity; strict clear-region violation. | Report it separately. If cleanup is authorized, preserve meaningful wisps/glow and recheck; never threshold the whole image blindly. |
| Pale/dark fringe after export or scaling | Matte color contamination or alpha/compositing handling may be involved. | Compare raw/final edge RGB and alpha, resizing behavior and straight/premultiplied conventions in the actual renderer. |

## Recovery limits

For ordinary source-over composition over an opaque backdrop:

`C = a * F + (1 - a) * B`

A flattened RGB pixel supplies three observed values but leaves foreground
color plus alpha unknown. Even a known backdrop does not make the original
foreground uniquely recoverable without assumptions. This follows from the
[W3C compositing equation](https://www.w3.org/TR/compositing-1/#simplealphacompositing).
Color keys and segmentation can therefore damage mixed pixels in glass, hair,
glow and sheer fabric or retain the original matte color. A checkerboard is
particularly troublesome because its contamination varies across the image.

An explicitly reviewed matte can work for a simple closed opaque silhouette
when that construction is authorized. It does not establish a general repair
for transparent walls, enclosed net holes, highlights or fine wisps. Preserve
the raw return and identify the result as reconstructed, not native alpha.

Prefer a clean native-alpha result. When it fails, choose a focused fresh
generation, clean-reference edit, separate material layers, or reviewed authored
coverage that fits the brief and permissions. Do not repeatedly feed a baked
checkerboard back into edits and assume interior checks will disappear.

Two perfectly registered images of the same foreground/coverage over known
different backgrounds can constrain simple alpha reconstruction. Two separate
AI generations do not guarantee identical geometry, lighting or pixels, so
black/white re-generation is not exact recovery.

One scalar alpha also cannot generally reproduce wavelength-dependent colored
transmission or spatial refraction of a changing scene. RGBA is a compositing
approximation; an application may need actual material/rendering support.

## Tool capability boundary

In the investigated host session, the built-in ImageGen tool exposed a prompt
and references, without background, format or model controls. Other interfaces
may expose more. Inspect the actual available schema; never fabricate a flag,
identify a hidden model, or claim the hosted generator was fixed by a local
validator. Check current official documentation before any API implementation.

Keep acceptance and production approval separate. Numeric PASS covers only the
declared technical expectations. Visual/material review and any project-specific
approval remain necessary; a retry limit is not permission to ship a failure.
