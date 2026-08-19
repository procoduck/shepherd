# Proof: prewarm and lazy recompute race safely

## Red run
Replacing the conditional write with plain `UpsertServeCache` would allow a stale prewarm or lazy recompute to clear a dirty mark created during the in-flight recompute. The named race integration test is the red proof for that temporary mutation.

## Green run
The named integration test passes with conditional writes and singleflight coordination: recompute is serialized per collector and a newer dirty mark is not cleared by an older write.
