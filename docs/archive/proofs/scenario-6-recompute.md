# Proof: Scenario 6 Recompute (FS-1 — standing regression proof for v1.3 P0.1)

## Test covered
- Scenario 6: enable pipeline → poll served-config → verify declare block present

## Red run

**Mutation:** Comment out `go h.recomputeOrgCaches(...)` in the pipeline enable handler
(`internal/mgmtapi/pipelines.go:setEnabled`) AND comment out the singleflight-gated
lazy recompute in `GetConfig` (`internal/agentapi/service.go`).

**Expected failure:**
```
scenario 6: enable pipeline → served config contains declare block
  expect.poll: content did NOT contain 'declare "pipe_fs_pipe_...'
  Timeout: 15000ms
  — serve cache was never recomputed after enable
```

## Green run

With both recompute paths active (handler prewarm + lazy GetConfig), the serve cache
is recomputed within one poll cycle after `forceRecompute()` is called. The `declare`
block appears in the served content and the test passes.

## Implementation location
`internal/agentapi/service.go:GetConfig()` (lazy path with singleflight)
`internal/mgmtapi/pipelines.go:setEnabled()` (prewarm goroutine)
