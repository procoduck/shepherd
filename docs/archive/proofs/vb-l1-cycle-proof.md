# VB-1 L1 Cycle Detection — Red Run Proof

**Test file:** `web/src/visual/l1.test.ts`  
**Invariant:** L1 cycle detection rejects graphs with cycles before they can be saved.

## What the test asserts
`validateGraph` with a 3-node cycle (A→B→C→A) returns a diagnostic with `code: 'cycle'`.

## Red run: how to break it
Change `detectCycle` in `web/src/visual/l1.ts` to always return `false`. The 3-node and 2-node cycle tests then receive no cycle diagnostic and fail.

## Why this matters
Without cycle detection, users can create loops in their pipeline graph. The L1 gate prevents those graphs before later validation and saving.
