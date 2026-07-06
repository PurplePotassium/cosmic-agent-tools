# Realm Survivors GDD — reference snapshot (provenance)

`realm-survivors-gdd-reference.md` (next to this file) is a **verbatim, reference-only
snapshot** of the Realm Survivors Game Design Document. It is kept so a future GDD
revision can be **diffed against this snapshot** before re-authoring Cosmo Canyon Ready
Specs — you don't have to re-scan the project to see what changed.

> ⚠️ Reference only. Cosmo Canyon has **no GDD import / splitter** (removed 2026-07-02);
> the loop authority remains the **Ready Spec set** in the Asset Browser. This file is a
> human aid for the operator, not wired into the loop.

- **Source doc:** https://docs.google.com/document/d/1Xbjc8CaeE5COjBxVWEssC2di3j8AyKBkxPPZ-lNKshs/edit
- **Doc id:** `1Xbjc8CaeE5COjBxVWEssC2di3j8AyKBkxPPZ-lNKshs`
- **Export URL (txt):** `https://docs.google.com/document/d/1Xbjc8CaeE5COjBxVWEssC2di3j8AyKBkxPPZ-lNKshs/export?format=txt`
- **Fetched:** 2026-07-03
- **Specs authored from it:** 21 Ready Specs (see `control/assets/*/meta.json`, kind=spec) + 77 image + 16 audio placeholder assets.

## Re-fetch + diff (what changed since this snapshot)

Run from `C:\Vibes` (the reference file is verbatim, so the diff shows only real content changes):

```bash
curl -sL "https://docs.google.com/document/d/1Xbjc8CaeE5COjBxVWEssC2di3j8AyKBkxPPZ-lNKshs/export?format=txt" \
  | diff - cosmo-canyon/docs/realm-survivors-gdd-reference.md
```

To re-import GDD changes: run that diff, then update/add the affected Ready Specs in the
Asset Browser to match the delta, and refresh this snapshot (overwrite the `.md` with the
new fetch) so the next diff is against the current baseline.
