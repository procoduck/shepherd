# Proof: local admin boot validation

## Red run
With local admin enabled and no password hash, configuration loading fails with the required `password_hash` validation error.

## Green run
The validation is implemented in `config.Load`; the configuration package tests pass.
