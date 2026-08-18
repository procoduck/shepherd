# Proof: local admin login session

## Red run
Before the feature, the local login route and local session source did not exist.

## Green run
The local handler creates a cookie-backed session with source `local`, app-admin privileges, and the configured TTL.
