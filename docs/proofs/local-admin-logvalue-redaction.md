# Proof: local admin LogValue redaction

## Red run
Logging a local admin configuration could expose the configured password hash.

## Green run
`LocalAdminConfig.LogValue` emits `[REDACTED]`; the named test confirms the hash value is absent.
