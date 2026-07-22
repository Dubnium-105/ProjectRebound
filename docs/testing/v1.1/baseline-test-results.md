# V1.1 baseline test results

English | [简体中文](baseline-test-results.zh-CN.md)

Baseline commit: `af4343a4cdc0b60836a417c182d0c65c0917c197`

Execution time: 2026-07-21 12:53 (Asia/Hong_Kong)

Working directory: `C:\wksp\ProjectRebound\Backend`

Working tree: clean before execution, `main` is consistent with `origin/main`.

## Environment

```text
OS/ARCH: windows/amd64
Go: go1.26.2
go.mod language version: 1.25.0
CGO_ENABLED: 0
TEST_DATABASE_URL: not set
```

## Required baseline

### `go test ./...`

Result: **Passed**, exit code 0.

- Common tests passed for OpenAPI, deployment configuration, authentication, management, connectivity, migrator, game server, health check, middleware, observability, P2P, Relay registry/runtime and update modules.
- There are no test files for `cmd`, `cache`, and some compatible packages.
- PostgreSQL integration tests that depend on `TEST_DATABASE_URL` are skipped because the variable is not configured natively. Involving: admin, auth, connection, database, gameserver, p2proom, relayregistry.

### `go vet ./...`

Result: **Passed**, exit code 0, no output.

## Project existing additional checks

| Command | Result | Description |
| --- | --- | --- |
| `go mod verify` |pass| `all modules verified` |
| `python Tools/Docs/check_markdown_links.py` |pass| `MARKDOWN_LINKS_OK` |
| `bash -n Backend/scripts/*.sh Backend/deploy/deploy.sh Backend/tests/netem/run-relay-matrix.sh` |pass| `BASH_SYNTAX_OK` |
| `go test -race ./... -count=1` |Not executed successfully|Current Windows Go environment `CGO_ENABLED=0`, command exited before testing: `-race requires cgo`. CI's Linux runner will execute this.|
| `staticcheck ./...` |Not executed|`staticcheck` is not installed on this machine.|
| `golangci-lint run` |Not executed|`golangci-lint` is not installed on this machine.|

## Baseline limits and subsequent thresholds

1. This local baseline cannot replace CI’s Linux race test.
2. PostgreSQL integration tests are not executed on this machine; after Milestone 1 is started, isolated PostgreSQL/Redis must be used to run new migration and concurrent transaction tests.
3. This baseline did not run a real UDP Relay, netem, failover migration, backup/restore, or long-duration load. Those are dedicated gates for later milestones.
4. Uninstalled static analysis tools are explicitly documented and are not considered "passed". Before entering Release Gate, you should run on a CI fixed version or explicitly adopt the existing `go vet` + race strategy.

## Conclusion

The minimum code baseline required to start V1.1 modifications (`go test ./...` and `go vet ./...`) passed. Unexecuted items and integration test skipped items due to environmental limitations have been recorded and can be entered into Milestone 1, but it cannot be used to declare that the database, race or long-term stability tests of V1.1 have passed.
