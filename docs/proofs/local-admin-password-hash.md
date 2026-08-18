# Proof: local admin password hash

## Red run
Before the feature, no argon2id password hash or verifier was available.

## Green run
`HashPassword` output verifies for the original password and rejects a wrong password in the named auth test.
