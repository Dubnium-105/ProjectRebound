English | [简体中文](README.zh-CN.md)

# Multiplayer hardware test delivery — 2026-09-08

The existing backend is on schema 48. The same r4 package reached first-person offline PvE on this Windows machine, including deployment and movement; a timely-deployment follow-up also produced Payload offline Playable with no timeout. Both closed with zero target processes remaining. Three-machine online admission and complete matches have not run. This package supports installation, login and managed native evidence collection; `native_authority_admission_verified=false` and `release_ready=false` remain unchanged. See the [final handoff receipt](final-handoff-receipt.json).

## Delivery files

Distribute the local file `C:/wksp/ProjectRebound/artifacts/hardware-test-20260908-r4/rebound-hardware-test-20260908-r4-windows-x64.zip` (23,487,655 bytes). ZIP SHA-256:

```text
73bff684e19d0644fe310fa833624150e5b7c9105013482198a90a1ce6651689
```

Toolbox source is `30148ad96e08ee4e26cd4972c557a4dee2340c21`; Payload source is `98f57092ce3b3ce5b0e8d24c56d8a82e66c3127d`. The package contains production UI, test-signed EXE/DLL, install/restore scripts, version checks and a collector, plus bilingual installation instructions and a three-machine matrix. It updates an existing complete Rebound runtime and does not contain the game. The test signer is not a default trusted root; no root certificate was installed and no production update was published. See the [distribution receipt](distribution-r4-receipt.json).

## Actual acceptance

| Item | Observed result and evidence |
|---|---|
| Backend and CI | Deployed images correspond to `584e000`; the original CI succeeded in 11 jobs, deployment in 4 jobs, and the database migrated from schema 43 to 48. See the [deployment receipt](deployment-receipt.json). |
| Server storage cleanup | Removed 77 unused project images and freed 2,045,927,424 bytes; data volumes were retained. Build-cache cleanup reclaimed 0 bytes and is not counted as savings. |
| Local storage cleanup | Removed the old Rust debug incremental cache after checking paths and build processes. C volume free space increased by 32,452,939,776 bytes to 34,377,019,392 bytes; hashes of all four current protected artifacts remained unchanged. See the [cleanup receipt](local-cache-cleanup-receipt.json). |
| Native login failure | The existing Compose override lacked Redis `EVAL/EVALSHA`, preventing game-ticket consumption. The live permissions were backed up and corrected without restarting Redis/Meta or changing credentials. See the [repair receipt](meta-acl-repair-receipt.json). |
| Deployment guard | `0aec1e7`, `b66bc66` and `7c130f3` validate the final merged configuration before deployment, checking complete commands and least privilege without echoing configuration values. The actual corrected configuration passed and the actual stale configuration was rejected. Public commit `129f1ca` contains only these 5 changed files; all 11 jobs in its [latest CI](https://github.com/Dubnium-105/ProjectRebound/actions/runs/34205921914) succeeded. See the [actual job receipt](evidence/final-ci/ci-sanitized-receipt-34205921914.json). Existing deployed application images were not replaced for this follow-up. |
| Local r4 startup | All 45 preflight checks passed; production UI rendered and an existing Steam-linked session was observed. A fresh login challenge was not forced. |
| Local PvE | The first run completed actual Deploy and W movement, but slow selection triggered `playable_pawn_timeout`. Following timely deployment in the second run, the client recorded `Offline PvE travel reached Playable` with zero occurrences of either readiness timeout; the server recorded native spawn, possession and StartMatch. The second run's post-W capture was already after death, so it makes no independent movement-pass claim. See the [second-run receipt](evidence/runtime-r4-fast/r4-fast-runtime-receipt.json). |
| Exit cleanup | Game, server and Toolbox closed normally; the final target process count was 0. See the [original structured runtime receipt](evidence/runtime-r4-after-acl/r4-after-acl-final-runtime-receipt.json). |
| Three-machine online/Playable | `NOT_RUN`; this build's native capability gate remains unverified. Offline PvE, bot counts and CI do not substitute for multiplayer native-game evidence. |

Actual earlier r3 and pre-repair r4 failures, plus two failed read-only probes, remain in the evidence chain. Later success did not overwrite them. The [runtime byte verification](runtime-evidence-validation.json) checked 25 local raw files; screenshots and raw game logs remain local and were not placed in the public CI branch.

The [timeout review](evidence/runtime-r4-after-acl/r4-timeout-review.json) identifies a 90-second Payload pawn-readiness deadline. The earlier run took 215.844 seconds from the PvE button to Deploy; timeout cleared its pending scope, so later controllable gameplay did not recover Payload readiness. The timely-deployment follow-up took 110.6146 seconds from button to Deploy, but the button is not the native timer's start. Raw logs have no per-line timestamps, so no measured native interval below 90 seconds is claimed. Its actual Playable line, zero timeouts and cleanup evidence were confirmed by [verification of 23 raw files](fast-runtime-evidence-validation.json). Slow selection remains a limitation: complete role selection and Deploy promptly, and record corresponding timeouts separately during three-machine testing.

The [latest CI log summary](evidence/final-ci/ci-log-summary-34205921914.json) records the raw log hash, one `DEPLOY_SOURCE_TEST_OK` marker and successful scan steps for five images. The log has no parseable vulnerability-count summary; scan-step success is recorded without inferring zero vulnerabilities.

## Starting with three machines

1. A hosts and B/C join. Each machine uses the fixed Boundary version and its owner's Steam account. Verify the ZIP hash above and extract the entire package.
2. Close the game and Toolbox, then follow the packaged `README.md` to run `Install-StrictPayload.ps1` and `Check-ThisMachine.ps1`. Record missing loaders/runtime files as real blockers.
3. Log into Toolbox normally, join one authoritative lobby and mark each seat ready. The host selects native admission evidence collection, using the current real frozen roster, allocation and Grant.
4. Follow the [three-machine matrix](TEST-MATRIX.md) to record two unique remote member seats, each machine's last stage, errors and cleanup. Start another round only after complete cleanup.
5. Return only the packaged collector JSON and sanitized observations. Exclude platform identities, credentials, raw configuration and backups. The package does not upload automatically.

Successful collection does not enable ordinary complete matches. Record `native_admission_unverified` if encountered. Use `BLOCKED` for missing machines, dependencies or services and `NOT_RUN` for unexecuted checks. Empty Tokens, disabling the strict roster and console `open` must not manufacture passing results.

The [runbook/source review](evidence/runbook-review/r4-runbook-source-review.json) is static evidence. Collection supports different lobby sizes, so a two-person lobby can also collect successfully. The three-machine item still requires manual confirmation of receipts for both unique B/C MEMBER seats in the current signed roster; the success message alone is insufficient for PASS.

## The 52-item record and remaining limits

Each item retains its original status, dependencies, implementation commits and current evidence: [backend/CI delta](backend-ci-delta.json), [client delta](client-native-delta.json), [r4 correction](client-native-delta-r4.json), and [current Meta/runtime delta](runtime-meta-delta.json). The original 10 PASS / 30 PARTIAL / 12 BLOCKED snapshot is historical; this run did not mark all 52 items passed.

Only this machine is currently accessible for real execution. Three-machine online admission, complete matches, reconnect/migration and next-round reuse still need evidence from the three machines the user can arrange. The existing HGH node closed SSH and was not updated; it is outside this run's primary/gateway deployment matrix.
