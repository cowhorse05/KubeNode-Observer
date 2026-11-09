# guest-relay simulation

Created 2026-11-05 from this repository's Guest OpenAPI contract; no separate owner/workspace exists or was searched. No cloud publication or account binding.

## Purpose

Run the production Node Relay against the Guest HTTP dependency, exercising networking and failure handling; not a substitute for real Guest BPF/Pod-network validation.

## Execution

```bash
postman simulation run postman/simulations/guest-relay/guest-relay.sim.yaml --proxy --proxy-port 4901 --no-history --log-format json
KNO_GUEST_SIM_PROXY=http://127.0.0.1:4901 go test ./internal/guest -run TestRelayPostmanSimulation -v
```

Stop the owned simulation process with SIGINT. The test points only its HTTP transport at the proxy; production ignores proxy environment variables.

## Assumptions

- (reasoned 2026-11-05) The mock covers read-only windows and error shapes, not scheduler accuracy, identity attestation or Kata availability. Those need live tests.
- (reasoned 2026-11-05) `comm` selects a fault only in this stand-in; production treats it as an exact process-name filter. See the mock handler for fixtures.

## Observations

<!-- begin:observations -->
2026-11-05: the sole Guest member served every tested failure shape; the production Relay returned expected success/busy/upstream/identity/timeout statuses. No member was silent. Live BPF/network claims are intentionally separate.
<!-- end:observations -->

## What didn't work

- 2026-11-05: CLI 1.86 rejected the initial mock with `Manifest version must be 1`; its manifest also requires `scenarios[].path` (or mockSrc), not a top-level default file. The committed config includes both requirements, verified by the subsequent passing run.
