# PR Description — Fix flaky sig-cli e2e auth drops by constructing ClientSet from persisted framework config

## Summary
This PR fixes an intermittent auth-related flake in sig-cli e2e tests where framework operations in setup/cleanup unexpectedly run as `system:anonymous` and fail with `forbidden` errors.

The fix ensures the framework creates `ClientSet` from the persisted framework config (`f.clientConfig`) rather than a short-lived local config variable in `BeforeEach`.

Closes #140070.

## Problem
sig-cli tests sometimes pass main assertions but fail in setup (`BeforeEach`) or cleanup (`DeferCleanup`) with errors like:
- `User "system:anonymous" cannot create resource "oauthclients"...`
- `User "system:anonymous" cannot delete resource "namespaces"...`

These failures are intermittent and appear under high-concurrency/high-memory-pressure runs.

## Root Cause
In `test/e2e/framework/framework.go` `BeforeEach`, the framework:
1. Loads config into local variable `config`
2. Copies config to `f.clientConfig`
3. Creates `f.ClientSet` from local `config` instead of persisted `f.clientConfig`

Because `ClientSet` is not built from the persisted config pointer, auth material can be lost/invalidated in some GC/cache timing conditions, causing fallback to anonymous identity during later calls.

## Proposed Fix
Update `BeforeEach` to construct the client from the persisted config:

1. `config, err := LoadConfig()`
2. tune QPS/Burst
3. `f.clientConfig = rest.CopyConfig(config)`
4. `f.ClientSet, err = clientset.NewForConfig(f.clientConfig)`  ← use persisted copy

This keeps framework-owned config strongly referenced and consistent across test lifecycle phases.

## Why this is safe
- Behavior is aligned with framework intent: one authoritative config stored on `Framework`.
- No API surface changes.
- Change is narrowly scoped to client construction source.

## Risk Assessment
- **Risk**: very low; source pointer for client construction changes to already-copied equivalent config.
- **Potential side effect**: none expected beyond removing auth drop flakes.
- **Mitigation**:
  - targeted regression test around `BeforeEach` client construction path.
  - run sig-cli focused e2e jobs to validate reduced flake rate.

## Validation Plan
- Unit/regression validation in e2e framework package for client construction path.
- Prow / CI runs for affected sig-cli suites:
  - verify absence of `system:anonymous` setup/cleanup failures across repeated runs.
- Stress-oriented reruns (parallelism/high GC pressure) to confirm flake reduction.

## Expected Outcome
- Setup and cleanup operations keep authenticated identity.
- Intermittent `system:anonymous forbidden` failures in sig-cli flakes are eliminated.

## Checklist
- [x] Full local PR description drafted.
- [ ] Framework fix implemented in `test/e2e/framework/framework.go`.
- [ ] Regression coverage added/updated.
- [ ] Repeated CI signal confirms flake reduction.
