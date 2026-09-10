# Install or share ImageGen Transparency

Copy or extract the entire `imagegen-transparency` folder into the receiving
agent's configured skill directory. Keep `SKILL.md`, `agents`, `scripts`,
`references`, `tests`, this file and `requirements.txt` together.

For a Codex installation using `~/.agents/skills`, the resulting entrypoint is
`~/.agents/skills/imagegen-transparency/SKILL.md`. Use the receiving harness's
configured directory if different. Reload skills or start a new session after
installation. An agent without skill discovery can read `SKILL.md` directly.

Invoke explicitly with `$imagegen-transparency`, or request transparent asset
generation/review; automatic selection is enabled. For a consuming project
that always requires alpha assets, add this to its agent instructions:

> Before generating or accepting raster assets that require transparency, use
> the imagegen-transparency skill. Verify raw and final alpha, full edges,
> enclosed holes and meaningful material opacity over contrasting backgrounds.

The inspection scripts need Python and Pillow. Use the receiving environment's
dependency policy; if installation is needed and authorized, run from this
skill folder:

```text
python -m pip install -r requirements.txt
python -B -m unittest discover -s tests -v
```

The package was tested with Python 3.12 and Pillow 12.2. Tests use temporary
synthetic images and make no network calls. Generation itself uses the host's
authorized image tool; the package contains no generator client or credentials.

The ZIP includes no project art, absolute local paths, private conversation
history or dependency on an external knowledge store. Generated diagnostic
reports identify their local input/output paths and hashes for provenance;
review those reports before sharing them separately.
