# Schema 49 deployment preparation

English | [简体中文](README.zh-CN.md)

Status: **BLOCKED — preparation only.** The schema 49 deployment has not been dispatched.

## Candidate and CI

- Candidate commit: `5fb54ce2832e152b5ea4d84c5656f2ad8bb11f37`.
- [CI run 35627019586](https://github.com/Dubnium-105/ProjectRebound/actions/runs/35627019586) succeeded: all six test jobs and all five image jobs passed, including SBOM, provenance, and high/critical vulnerability scanning.
- The previous V1.1 run at `33d4bc6` succeeded. This round only corrects the fixture `expires_at` value.
- The existing [match-lifecycle delivery receipt](../match-lifecycle-20260921/DELIVERY.md) and [verification record](../match-lifecycle-20260921/VERIFICATION.md) describe the flow-fix evidence.

## Production gate

Production remains on schema `48` and running revision `8069d5e1126b8a585610b232e721aee45c57ee88`; the schema 49 deployment has not been dispatched.

The primary public entry is blocked: HAProxy is failed and TCP 80/443 are not listening. The deploy user has no non-interactive sudo access, and the default root SSH key is rejected. Administrative SSH access or manual HAProxy recovery is pending.

The preflight backup is `/opt/projectrebound-deploy/backups/projectrebound-20260921T160028Z.dump`, SHA-256 `263bbd5006d731f1c21591e9045933642f069795cd49169af108878e24e1d47b`, 11,017,457 bytes, mode `0600`. Preserve the historical P2P attempt whose cleanup gate is `MATCH_ATTEMPT_CLEANUP_PENDING` and affects two players; do not force-clear the native cleanup gate. Zero active connections is not proof of native cleanup. The recorded preflight source is `.tmp/schema49-deploy-20260921/preflight-extended.json`.

## Deployment conditions

1. CI and images are ready; see the [deployment receipt](deployment-receipt.json) for exact commits and digests.
2. Restore and verify the public entry, health/SNI routing, and Relay before dispatch.
3. Confirm the backup, then apply migration 49.
4. Verify the schema 49 deployment before physical acceptance.

After schema 49, rolling back only the old schema 48 image is invalid; recovery requires a full database restore or a forward fix.

## Acceptance boundary

A complete native match, cleanup, and next lobby in all four modes remain **NOT_RUN**. `release_ready=false`.
