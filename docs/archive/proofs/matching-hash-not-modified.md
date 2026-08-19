# Proof: matching hash → not_modified

## Red run
Before the fix, the recomputed response path did not provide the required stable cache result for a subsequent poll. The named integration test would fail because the matching-hash request was not marked `NotModified` or the counter did not advance.

## Green run
The named integration test passes: a second request with the returned hash is `NotModified` and increments `GetConfigTotal{result="not_modified"}`.
