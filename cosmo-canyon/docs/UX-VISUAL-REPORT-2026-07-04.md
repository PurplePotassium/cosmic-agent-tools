# Realm Survivors (cosmo-canyon/game) — Usability + Visual Report

2026-07-04. Sources: live playthrough screenshots (headless puppeteer tour, 390×844 mobile portrait,
seeds 42/43 — menu → whoareyou → story → run → level-up → chest → boss → victory) + full render/sim
code audit (54 findings). Screenshot tour scripts reusable: pattern in `orchestrator/snapshot.mjs`.

Verdict: sim + juice plumbing is strong (screenshake, hit-stop, particles, damage numbers, per-biome
synth music all exist). The gap is **presentation**: the arena looks like a debug void, the level-up
draft looks like a debug menu, and the big moments (level-up, evolve, boss, victory) resolve with no
ceremony. Below, ordered by player impact.

---

## 1. Level-up / draft ceremony (the "pomp" ask) — HIGHEST PRIORITY

What exists today: world-side flare + white flash + `levelup` jingle fire in stage.ts:570 when XP
threshold hits; then `LevelUpUI.sync()` (render/levelup.ts) pops a static 0.72-alpha dim + 18px
monospace title + three flat dark cards, all appearing in one frame. Picking a card hides the UI the
same frame. No entrance animation, no hover state, no pick feedback, no exit flourish.

Recommended sequence (all doable with existing utilities — easeOutBack from outcome.ts:45, particle
pool stage.ts:110, trauma shake, audioManager):

1. **Slam-in banner**: "LEVEL UP!" scales in with easeOutBack over ~0.35s, gold, 2–3× current size,
   with radial rays behind it (reuse triggerFlair rays). Chest drafts: chest bounces + lid-pop burst.
2. **Card deal**: cards stagger in 80ms apart, sliding up + overshooting (easeOutBack), not appearing
   simultaneously. Rarity drives extras: rare = purple edge glow pulse; legendary/EVOLVE/FUSION =
   gold shine sweep across the card + slow border pulse. Today an EVOLVE card is visually identical
   to a stat card except border color — the single rarest moment in the genre reads as nothing.
3. **Hover/press states**: cards have no pointerover handler (levelup.ts:242 only wires pointerdown).
   Add brighten + scale 1.03 on hover, press-down on click. Keyboard picks (1/2/3) should flash the
   card, not just close the menu.
4. **Pick confirmation**: chosen card zooms toward player + dissolves into sparks; 150ms hold before
   unpausing; distinct "confirm" SFX (audioManager extends per-key). Unchosen cards fall away.
5. **Post-pick toast**: floating "+1 Holy Symbol Lv3" text over the player using the existing dmgPool
   floating-text system (stage.ts:118) so the reward is visible after the menu closes.
6. **Evolve/fusion deserve a cutscene beat**: freeze world, both weapon icons fly together, flash,
   result icon slams down with trauma 0.5 + rays. Currently identical flow to a common stat pick.

Same treatment scaled down for: chest open (chest sprite already animates 4 frames — add gold light
column + coin sparks), boss defeat, run victory.

## 2. Arena is a flat void — biggest VISUAL gap

Screenshots 04/07/13/15: ground = uniform dark navy + faint grid dots, zero props, zero texture
variation, light vertical bands at arena edges. Story beat promises "the green"/treeline; the arena
that follows is a night-void grid. The painted story-beat art sets a bar the gameplay screen
instantly breaks.

- Add a real ground layer per biome: tiled grass/dirt texture (even 2-tone checker + scatter decals
  — pebbles, tufts, flowers — beats grid dots), vignette darkening toward edges instead of hard
  bands.
- Scatter non-colliding doodads (rocks, stumps, bones) from the seeded RNG so runs feel placed.
- Brighten the base palette toward the biome color; "grasslands" should read green even at night.
  2d-game-art-direction rule: enemy/player silhouettes stay readable if ground stays low-saturation,
  low-contrast — but low-contrast ≠ featureless.

## 3. Boss presentation

- **Boss spawns offscreen with no direction indicator** (shots 14/15: bar says SKELETON COMMANDER,
  nothing on screen). Add an edge-of-screen arrow/skull marker until boss enters view.
- Boss nameplate text ~7px, unreadable (hud.ts name over bar). Bigger name, brief center-screen
  "⚔ SKELETON COMMANDER" banner on spawn (the trauma+zoom punch already fires — pair visuals).
- Phase ticks at 66%/33% (hud.ts:453) unlabeled — flash the bar + SFX on phase cross.
- **Boss/miniboss death has no in-world feedback**: combat.ts:284 logs the fxEvent but stage.ts
  drainFxEvents has no handler. Add mega death-pop, slow-mo beat (~0.5s at 0.3× via hit-stop path),
  gold fountain, then victory flow.
- Player death equally silent in-world (defeat cue only plays on the outcome screen, outcome.ts:212).

## 4. Victory/defeat screen

Shot 09: parchment panel good frame, but stats text is dark-olive-on-parchment (low contrast, ~11px),
VICTORY! muddy gold, bottom half of the panel empty, everything appears in one pop.

- Count-up stat reveal (numbers tick up, stagger rows 100ms) — cheap, huge feel.
- Gold payout: coin-burst + count-up into a wallet counter, not a static "+560 GOLD" line.
- Confetti/rays behind VICTORY!; red/desaturated variant for defeat.
- Fix text contrast: near-black ink on parchment, larger stat font, fill the empty bottom half
  (weapon build recap icons — data already in pause.ts build summary).

## 5. Scene flow + transitions

- All scene swaps are instant visibility flips (menu→whoareyou→story→run→outcome). One shared 0.2s
  fade-to-black scrim (tweened Graphics on top of app.stage) fixes every transition at once.
- **HUD bleeds into non-run scenes**: "00"/HP sliver visible top-left during whoareyou + story beats
  (shots 02/03/12). Hide HUD root outside runs.
- Story-beat dialog box: text overflows/clips the frame (shot 12, last line cut), white-on-brown low
  contrast, speaker label tiny, "tap to continue" nearly invisible. Enlarge box, wrap-measure text,
  add blinking ▼ continue chevron. Portrait art (shot 12) is a strong asset — box should match its
  quality.
- Main menu is dead space (shot 01): title floats in void. Reuse story-beat painted art as blurred/
  darkened backdrop + subtle title bob/glow; menu instantly stops looking like a placeholder.
- Settings button dead-ends (mainmenu wires onSettings; main.ts never shows a settings UI). Ship
  minimal panel: music/SFX volume, screen-shake toggle, wipe-save relocation.

## 6. HUD readability (mobile portrait)

- HP "300/300", "Lv 1", "kills" ~10px monospace (hud.ts:177 area) — below comfortable mobile
  minimum; bump to 13–14px, add 1px dark outline.
- XP bar = 3px blue sliver at very top; barely registers even on level-up punch. Thicken to ~8px,
  brighter fill, glow pulse when near-full.
- XP gems = small dark-green dots on dark ground (shot 04). Brighten + slight pulse; rarity tiers by
  color/size.
- "BOSS DEFEATED" toast + miniboss banners tiny (shot 06). Center-screen toasts for milestone events.
- Loadout badges: empty slots read as noise circles; dim them further or show "+" hint. Poison
  droplet + level pips too small to parse in motion.
- Ability cooldown buttons: no numeric countdown or READY flash (hud.ts:80) — sweep angle only.

## 7. Menus/UX (from code audit, verified)

- Level-up cards: no "press 1–3" affordance beyond small [n] tags; no hover (see §1).
- whoareyou: two floating cards in a large empty parchment (shot 02) — no playstyle blurb, no
  selected-state preview. Fine for 2 choices, won't scale.
- Locked characters invisible rather than teased (charselect.ts:652) — show silhouette + unlock cost.
- Full-party "+ SUPPORT" button still looks clickable when full (charselect.ts:529).
- Pause: no ESC binding (button only), corner pill easy to miss on first run.
- Journal button reuses the settings icon (charselect.ts:93).

## 8. Audio coverage gaps (synth manager solid, per-event map thin)

Missing SFX: chest pickup (silent draft trigger, combat.ts:265), boss/miniboss death, player death
in-world, reactive weapon triggers (Tower Shield/Smoke Bomb), poison application, destructible break,
boss phase transitions, pause/unpause whoosh, card hover/pick (see §1). All wire through existing
`audioManager.playSfx` fallback-synth path — content work, not architecture work.

## 9. Prioritized quick wins

| # | Change | Cost | Payoff |
|---|--------|------|--------|
| 1 | Level-up ceremony (banner slam, card deal, hover, pick flourish) | M | Core loop feels 2× better |
| 2 | Ground texture + doodads per biome | M | Kills "debug void" look |
| 3 | Boss offscreen arrow + spawn banner + death slow-mo | S–M | Climax reads as climax |
| 4 | Shared fade-to-black scene transition | S | Whole game feels assembled |
| 5 | Victory count-up + contrast fix | S | Run end = reward |
| 6 | HUD font bump + XP bar thicken + hide HUD in menus | S | Readability + bug fix |
| 7 | SFX fill-in (chest, boss death, player death, reactives) | S | Cheap juice |
| 8 | Story dialog box: bigger, wrap fix, continue chevron | S | Matches its own art |
| 9 | Settings panel (volume, shake toggle) | S | Removes dead button |

Reusable systems already in the codebase for all of the above: easeOutBack/easeOutCubic tweens
(outcome.ts:45, storybeat.ts:77), pooled particles (stage.ts:110, cap 120), floating-text pool
(stage.ts:118, cap 40), trauma screenshake + zoom-punch (stage.ts:94/142), flare/rays
(stage.ts:570), audioManager synth+file fallback (audio.ts:583). No new dependencies needed.

## Evidence

Screenshots in [`ux-shots-2026-07-04/`](ux-shots-2026-07-04/): 01 main menu (dead space),
02 whoareyou (+ HUD bleed top-left), 03/12 story beats (art strong, dialog box clipped),
04/13 arena void, 05 level-up draft (flat cards), 06 chest draft, 09 victory (low-contrast stats),
14/15 boss offscreen with no indicator.
