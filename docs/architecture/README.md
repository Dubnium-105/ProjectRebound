# Architecture documentation

English | [简体中文](README.zh-CN.md)

- [System overview](overview.md): components, trust boundaries, control flow, and data flow;
- [Complete P2P online architecture](online-architecture.md): rooms, connection coordination, direct paths, Relay, BattleLog, state machines, deployment, and current implementation boundaries;
- [Community VNT node online target architecture](vnt-community-online-architecture.md): community nodes, room isolation, VNT bootstrap, ToolBox/VNT-Node, security boundaries, and migration from the current Relay path;
- [Authentication and sessions](authentication.md): player identity, token rotation, and risk controls;
- [Relay protocol V2](relay-protocol.md): UDP BIND, authenticated packets, and MTU;
- [Relay failure migration](relay-migration.md): state machine, events, and consistency boundaries;
- [Runtime command framework](command-framework.md): named-pipe protocol between the desktop browser and payload.
- [CommandFramework C++ implementation analysis](command-framework-code-analysis.md): function-by-function behavior, ownership, defects, security, and test guidance;
- [MetaServer architecture](metaserver.md): Go services, persistence, identity, scheduling, and dynamic Relay discovery;
- [MetaServer native protocol](metaserver-native-protocol.md): TLS tunnel, framing, Gate, and confirmed RPC boundary;
- [MetaServer threat model](metaserver-threat-model.md): assets, controls, residual risks, and security gates.

Endpoints are defined under the [API documentation](../api/README.md) and machine-readable contracts. Production procedures are under [operations](../operations/README.md).
