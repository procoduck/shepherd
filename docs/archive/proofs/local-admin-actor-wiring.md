# Proof: local admin actor wiring

## Red run
Session middleware did not propagate an actor identity into request context.

## Green run
OIDC sessions use email actors and local sessions use `local:<username>` via the shared auth context helpers.
