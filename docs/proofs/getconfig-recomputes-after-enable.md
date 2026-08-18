# Proof: GetConfig recomputes after enable

## Red run
Before the fix, a dirty serve cache was read without a per-collector coordinated lazy recompute. The named integration test would fail because the response did not contain `recompute-pipeline` or the cache remained dirty.

## Green run
The named integration test passes: lazy `GetConfig` recomputes the cache, serves the pipeline content, and the resulting cache is clean.
