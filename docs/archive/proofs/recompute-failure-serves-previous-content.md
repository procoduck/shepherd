# Proof: recompute failure serves previous content

## Red run
Before the fix, a lazy recompute failure could replace the response with empty content instead of preserving the previously cached configuration. The named integration test would fail because the second response was empty or returned an error.

## Green run
The named integration test passes: after the cache is marked dirty and the enabled pipeline is removed, `GetConfig` remains successful and serves the previous non-empty content.
