# Proof: /api/me Contract Fix (FS-1 Work Item 0)

## Tests covered
- Scenario 2: unauthenticated / redirects to /login
- Scenario 3: /api/me unauthenticated returns 401 with error envelope
- Scenario 14: /api/me authenticated returns canonical fields

## Red run

**Mutation:** Revert `internal/mgmtapi/orgs.go` `Me` handler to the stub that returns
`200 {"actor":"anonymous","orgs":[]}` for unauthenticated requests.

**Expected failures:**
```
scenario 3: /api/me unauthenticated returns 401 with error envelope
  Expected: 401
  Received: 200
  
scenario 2: unauthenticated / redirects to /login
  FAIL — useMe returns null on 401; stub returns 200 with {actor:"anonymous"},
  so the SPA thinks the user is "logged in" (non-null me) and stays on /.
  The test expects the URL to match /\/login/.
  
scenario 14: /api/me authenticated returns canonical fields
  Expected: body to have property 'user_oid'
  Received: {"actor":"local:admin","orgs":[],"auth_method":"local"}
  (old stub returns "actor" not "user_oid")
```

## Green run

After fixing `Me` to:
1. Return `401` with `{"error":{"code":"unauthenticated",...}}` when no session
2. Return the canonical shape `{user_oid, email, display_name, is_app_admin, auth_method, orgs:[...]}` when authenticated

All three scenarios pass.

## Implementation location
`internal/mgmtapi/orgs.go:Me()`
