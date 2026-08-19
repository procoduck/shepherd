# Proof: conditional upsert refuses to clear newer dirty flag

## Red run
Replacing `UpsertServeCacheConditional` with unconditional `UpsertServeCache` in the recompute path would clear a dirty mark made by a newer reconciliation. The named database integration test is the red proof for that mutation: the unconditional variant loses the dirty state.

## Green run
The named integration test passes with the conditional SQL path: the recompute write only clears the flag when the cache is dirty at write time.
