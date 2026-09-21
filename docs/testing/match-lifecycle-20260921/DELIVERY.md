# r14b-flowfix delivery manifest

English | [简体中文](DELIVERY.zh-CN.md)

Implementation and component verification for both repositories is complete. The items below are local test artifacts; nothing was pushed or deployed, and no native match was played.

- Backend / Payload source commit: `101e15902a2a0bcfb6ae76e5c096e00ec78bba21`.
- Toolbox source commit: `8a641dc77d8e377abdbf139ed7c210960db2f149`.
- Backend requires schema **49**; strict admission remains `strict-roster-v2`, with `match-lifecycle-v1` added.
- Both archives passed per-file byte readback; `release_ready=false`.

## `artifacts/hardware-test-20260921-r14b-flowfix/ProjectRebound-r14b-flowfix-20260921-windows-x64.zip`

Size: 23,643,317 bytes; file count: 23.

SHA-256: `4b9cb706743f5d4db0da0c2bdff77a8223b47d77de148db773c255671a9a9389`.

## `artifacts/hardware-test-20260921-r14b-flowfix/ProjectRebound-r14b-flowfix-20260921-server-linux-amd64.tar.gz`

Size: 47,763,969 bytes; file count: 57.

SHA-256: `996496a945fca9d86f2c2d7f0737933160b68dc2a8504e03b42e96b8cbbe4959`.

The Windows archive contains the matching EXE / DLL, install and rollback scripts, license, and test instructions. The Linux archive contains control-plane, meta-server, edge-relay, and migration SQL for the deployment operator.

See [VERIFICATION.md](VERIFICATION.md) for test results and [README.md](README.md) for the four-mode physical acceptance matrix. Full-match acceptance with the matching Backend and three independent Steam accounts remains **NOT_RUN**.
