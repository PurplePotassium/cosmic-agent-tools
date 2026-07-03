// Cosmo Canyon — spec-core trigger unit test (topup-churn hardening, 2026-07-02).
// Run: node orchestrator/spec-core.test.mjs
import { computeTrigger, latchKeyFor } from "./spec-core.mjs";
let fail = 0;
const eq = (got, exp, msg) => { const g = JSON.stringify(got), e = JSON.stringify(exp); if (g !== e) { console.log("FAIL", msg, "\n  got", g, "\n  exp", e); fail++; } else console.log("ok  ", msg); };

// base snapshot: non-empty authority, no diff/blocked/audit trigger — isolates the topup branch.
const S = (over = {}) => ({
  authorityEmpty: false, authorityChanged: false, authoritySha: "AUTH_X",
  blockedIds: [], auditDue: false, readyCount: 5, latch: {}, ...over,
});

// latchKeyFor: topup now keys on the AUTHORITY sha (the fix), not readyCount.
eq(latchKeyFor("topup", S({ authoritySha: "AUTH_X", readyCount: 2 })), "AUTH_X", "topup latchKey = authoritySha (not readyCount)");
eq(latchKeyFor("diff", S({ authoritySha: "AUTH_X" })), "AUTH_X", "diff latchKey = authoritySha");
eq(latchKeyFor("blocked", S({ blockedIds: ["a", "b"] })), "a,b", "blocked latchKey = joined ids");

// topup FIRES when queue low AND latch != authoritySha (fresh authority / never asked).
eq(computeTrigger(S({ readyCount: 2, latch: {} })), { mode: "topup", latchKey: "AUTH_X" }, "low queue, no latch -> topup fires");
eq(computeTrigger(S({ readyCount: 2, latch: { topup: "OTHER_AUTH" } })), { mode: "topup", latchKey: "AUTH_X" }, "low queue, stale-authority latch -> topup fires");

// topup SUPPRESSED once latched on the current authority — THE CHURN FIX: draining readyCount 2->1->0 must
// NOT re-fire when the latch already holds this authority (old code re-fired once per readyCount value).
eq(computeTrigger(S({ readyCount: 2, latch: { topup: "AUTH_X" } })), null, "readyCount 2, latched on authority -> suppressed");
eq(computeTrigger(S({ readyCount: 1, latch: { topup: "AUTH_X" } })), null, "readyCount 1, same authority -> suppressed (no churn)");
eq(computeTrigger(S({ readyCount: 0, latch: { topup: "AUTH_X" } })), null, "readyCount 0, same authority -> suppressed (no churn)");

// queue not low -> no topup regardless of latch.
eq(computeTrigger(S({ readyCount: 3, latch: {} })), null, "readyCount 3 (>=3) -> no topup");
eq(computeTrigger(S({ readyCount: 9, latch: {} })), null, "readyCount 9 -> no topup");

// empty authority short-circuits EVERYTHING (no junk planning).
eq(computeTrigger(S({ authorityEmpty: true, readyCount: 0, latch: {} })), null, "empty authority -> null (no topup)");

// precedence: a real debounced diff still beats topup.
eq(computeTrigger(S({ authorityChanged: true, readyCount: 2, latch: {} })), { mode: "diff", latchKey: "AUTH_X" }, "diff precedence over topup");

console.log(fail ? `\n${fail} FAILED` : "\nALL PASS");
process.exit(fail ? 1 : 0);
