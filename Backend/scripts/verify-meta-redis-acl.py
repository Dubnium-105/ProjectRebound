#!/usr/bin/env python3
"""Validate the effective MetaServer Redis provision command.

The input is the JSON emitted by ``docker compose config --format json``.  It
is deliberately parsed as Compose data instead of searching the YAML source:
an operator supplied ``!override`` file can replace the canonical entrypoint.
No rendered environment value is included in diagnostics.
"""

from __future__ import annotations

import json
import shlex
import sys
from typing import Any, NoReturn


EXPECTED_GRANTS = frozenset(
    {
        "+@connection",
        "+get",
        "+set",
        "+del",
        "+unlink",
        "+eval",
        "+evalsha",
        "+expire",
        "+pexpire",
        "+ttl",
        "+pttl",
    }
)


def fail(reason: str) -> NoReturn:
    print(f"META_REDIS_ACL_CHECK_FAILED: {reason}", file=sys.stderr)
    raise SystemExit(1)


def service_entrypoint(document: Any) -> list[str]:
    if not isinstance(document, dict):
        fail("Compose config root is not an object")
    services = document.get("services")
    if not isinstance(services, dict):
        fail("Compose config has no services object")
    service = services.get("meta-redis-provision")
    if not isinstance(service, dict):
        fail("meta-redis-provision service is missing")
    entrypoint = service.get("entrypoint")
    if (
        not isinstance(entrypoint, list)
        or len(entrypoint) != 3
        or any(not isinstance(value, str) for value in entrypoint)
    ):
        fail(
            "effective meta-redis-provision entrypoint is not the canonical shell form"
        )
    if entrypoint[:2] != ["/bin/sh", "-ec"]:
        fail("effective meta-redis-provision entrypoint is not /bin/sh -ec")
    return entrypoint


def validate_command(entrypoint: list[str]) -> None:
    try:
        tokens = shlex.split(entrypoint[2], posix=True)
    except ValueError:
        fail("effective Redis provision command is not valid shell syntax")

    acl_positions = [
        index
        for index in range(len(tokens) - 1)
        if tokens[index : index + 2] == ["ACL", "SETUSER"]
    ]
    if len(acl_positions) != 1:
        fail("effective Redis provision command must contain one ACL SETUSER")

    acl_index = acl_positions[0]
    try:
        command_end = tokens.index("&&", acl_index + 2)
    except ValueError:
        command_end = len(tokens)
    arguments = tokens[acl_index + 2 : command_end]
    # The username and password are intentionally only checked by position.
    # Never echo their rendered values into a deployment log.
    if len(arguments) < 6 or arguments[1:3] != ["reset", "on"]:
        fail("ACL SETUSER must reset the user and enable it")
    if not arguments[3].startswith(">") or arguments[3] == ">":
        fail("ACL SETUSER password argument is missing")
    if arguments[4] != "~meta:*":
        fail("Redis ACL key scope must be exactly ~meta:*")

    grants = arguments[5:]
    if len(grants) != len(set(grants)):
        fail("Redis ACL grant list contains duplicate entries")
    if set(grants) != EXPECTED_GRANTS:
        missing = sorted(EXPECTED_GRANTS.difference(grants))
        unexpected = sorted(set(grants).difference(EXPECTED_GRANTS))
        # Missing names come from the static canonical set. Unexpected values
        # are untrusted Compose input and may contain a rendered secret, so
        # report only their count.
        details: list[str] = []
        if missing:
            details.append("missing=" + ",".join(missing))
        if unexpected:
            details.append(f"unexpected_count={len(unexpected)}")
        fail(
            "Redis ACL grants do not match the canonical least-privilege set ("
            + "; ".join(details)
            + ")"
        )


def main() -> None:
    try:
        document = json.load(sys.stdin)
    except (json.JSONDecodeError, UnicodeDecodeError):
        fail("docker compose did not emit valid JSON")
    entrypoint = service_entrypoint(document)
    validate_command(entrypoint)
    print("META_REDIS_ACL_CHECK_OK")


if __name__ == "__main__":
    main()
