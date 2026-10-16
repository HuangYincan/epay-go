# Authenticated observer health — SPEC-020

2026-10-16, epay-go #6 / Android #24. Health proves only current device authorization. POST /api/observer/v1/health uses a separate path-bound signature domain and the same locked credential validation and persistent device budget as event receipt. A committed health response creates no event/request/order/ledger/callback; repeated health envelopes recheck current authorization and consume budget.

Validation: full go test -race ./... and go vet ./... passed. Shared golden signature, strict body/header and cross-domain negatives, actual TLS, expiry/revocation/scope, persistent replay charging, business table counts, and deferred transaction commit failure are covered. Independent Standards and Spec review found no gateway regressions in the shared authorization transaction refactor.

Final Android emulator synthetic run used a clean PostgreSQL schema: 2 successful health calls, 2 events and 3 requests (first ACK body lost), 0 conflicts and redirects; 2 historical Android UNBOUND events remain local. Both reports explicitly set payment_verified=false. Bridge passed in 39.13s and cleaned its schema. No real payment or New API balance test is claimed.

Sanitized evidence and APK/bridge hashes: HuangYincan/epay-observer-android docs/testing/status-v1/evidence; report docs/testing/M2-status-health-report.md. Private lab /tmp/epay-observer-health-final is not committed. Fixed Go/PostgreSQL images and localhost-only fixture CA procedure follow pairing.md/background-testing.md. Upload AVD mode now additionally requires health_checks=2; production trust/authorization rules are unchanged.
