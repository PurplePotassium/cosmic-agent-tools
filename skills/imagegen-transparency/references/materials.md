# Materials: prompt additions and review targets

Choose the relevant row, not a universal opacity preset. These distinctions
complement the requested art style and registration; they do not override them.

| Asset | Specify in the prompt | Inspect / construct |
| --- | --- | --- |
| Empty potion bottle | Clear exterior and glass cavity; smooth partial alpha in glass; opaque stopper and label if present. | Backdrop must change through empty glass. Check walls/base for baked checker residue. Separate glass highlights from solid fittings when useful. |
| Filled potion bottle | Independent glass and liquid opacity; a distinct fill line; opaque cork. Explicitly say whether liquid is translucent or opaque. | Test liquid separately from empty upper glass. A bright glossy liquid can still be nearly opaque. Layer contents behind front-glass highlights if needed. |
| Fishing net | Truly clear enclosed mesh cells and handle openings; continuous fine cords with antialiased edges. | Inspect every cell at native and game size. A silhouette-only mask leaves filled holes; component cleanup may erase strands. |
| Hair | Clear gaps between locks; opaque broad masses; partial coverage for wisps and antialiasing. For a detached hair layer, exclude head/skin/backing. | Preserve fine strands and gaps on both dark and light backdrops. Validate on the actual wearer when fitting is in scope. |
| Clear bottle filled with candy | Opaque candies, including white ones; clear empty spaces between candies; translucent glass; opaque stopper. | Protect pale candy and highlights from key deletion. Distinguish a clear gap from another candy behind it. Use candy behind front glass if separating layers. |
| Eyeglasses | Opaque frames; either completely open lens areas or lightly translucent lenses, as designed. | Inspect both lenses, bridge and temple gaps. Check the face through the lenses, plus pale rims on dark backgrounds. Separate lens tint/highlights from frame when useful. |
| Filled potion with magical glow | Bottle/liquid behavior as above; smooth emitted light fading to alpha zero without a black rectangle or hard halo edge. | Evaluate glass, liquid and glow independently. A working glow does not establish working glass. A separate emission layer offers control when the renderer supports it. |
| Sheer cloth | Continuous partial alpha through thin fabric, denser overlapping folds/hem and preserved weave. | Check broad single-thickness panels as well as folds. A translucent-looking painted cloth can be almost opaque. Binary masking removes the sheer effect. |
| Lantern with stained glass | Opaque frame/lead; colored translucent panes; clear handle openings. State whether lit or unlit. | Check each pane color independently. Do not accept bright painted panes as proof of transmission. Separate frame, pane tint and optional light when supported. |

For translucent surfaces, say **"the eventual background visibly shows through
this named material via genuine partial alpha"**. Naming glass alone often
elicits the visual appearance of glass without useful background contribution.
Wording is not a guarantee; verify the decoded result.

Simple game sprites can intentionally approximate glass using mostly clear
areas with highlights and an outline. Ordinary RGBA does not refract a changing
background. Decide whether that approximation suits the brief; use rendering
support for refraction or dynamic colored transmission when it matters.

Avoid setting one opacity for an entire mixed-material sprite. Making a bottle
transparent globally also fades corks, labels, candy and other solid details.
Keep material ownership distinct when reconstructing an authorized failed asset.
