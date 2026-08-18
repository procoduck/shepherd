# Proof: ListAllClusters Fix (FS-1 Work Item 0)

## Test covered
- Scenario 5: cluster list returns all clusters with created_at

## Red run

**Mutation:** Revert `internal/mgmtapi/admin.go` `ListClusters` to call
`ListUnclaimedClusters` for both the default and `?unclaimed=true` paths.

**Expected failure:**
```
scenario 5: cluster list returns all clusters with created_at
  Assertion: body.items.some((c) => c.name === 'prod-eu-1')
  Result: FALSE — prod-eu-1 is claimed and therefore absent from ListUnclaimedClusters
```

## Green run

After adding `ListAllClusters` SQL query and updating the handler to call it for
the default (no `?unclaimed=true`) path, `GET /api/admin/clusters` returns both
claimed and unclaimed clusters. Scenario 5 passes.

## Implementation location
`internal/store/queries/clusters.sql` + `internal/mgmtapi/admin.go:ListClusters()`
