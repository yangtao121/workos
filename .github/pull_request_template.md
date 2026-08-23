## Scope

Describe the single module or vertical slice changed, including what is intentionally out of scope.

## Contract and ownership

- [ ] No process/schema ownership boundary changed, or an ADR is linked.
- [ ] Proto/manifest changes preserve v1 compatibility and generated files were refreshed.
- [ ] No provider-specific type escaped its adapter.

## Evidence

- [ ] `make check`
- [ ] `make test-integration` when PostgreSQL or process interaction changed
- [ ] User-facing cross-process behavior has E2E evidence
- [ ] Task record and `docs/status.json` reflect the actual state

Task record: `docs/tasks/...`
