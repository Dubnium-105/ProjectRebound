# ProjectRebound runtime directive framework

English | [简体中文](command-framework.zh-CN.md)

CommandFramework is a native named-pipe protocol between a Windows launcher and
the in-process game Payload. This repository maintains the C++ pipe server and
the wire contract. The deprecated in-repository .NET client has been removed;
client implementations live with their consuming frontend.

## Implementation location

- Payload server: `Payload/Communication/CommandFramework.h`, `CommandFramework.cpp`;
- Payload wiring: `Payload/dllmain.cpp`;

The consuming launcher generates a pipe name for each run and passes it to the
game via `-pipe=<name>`. The Payload creates `\\.\pipe\<name>`, then the
launcher connects.

## Frame format

Each message is a line of UTF-8 text:

```text
<command>\t<json>\n
```

- There is a Tab between the command and JSON;
- Each frame ends with LF;
- JSON must be an object;
- The maximum length of a single frame is limited by the Payload's current 64 KiB read buffer.

Example:

```text
ping\t{}
join\t{"ip":"203.0.113.10:7777","token":"..."}
debug\t{"action":"status"}
```

## Current command

| Direction | Command | Response | Description |
| --- | --- | --- | --- |
| Browser → Payload | `ping` | `pong` | Connection check |
| Browser → Payload | `join` | `join_ack` | Request to switch to the session specified by `ip`; `token` is currently reserved |
| Browser → Payload | `debug` | `debug_ack` |Execute the debug callback registered by Payload|
| Payload → Browser | — | `error` |Invalid JSON, missing fields, or unknown command|

`join` requires the non-empty string `ip`. Payload calls the Join callback in the listening thread and returns whether to accept the command; the actual joining result is still determined by the game connection process.

## Life cycle and concurrency

- Payload uses a single listening thread and Overlapped I/O;
- By default, if no data is read for 30 seconds, the current client will be disconnected and the pipeline will be rebuilt;
- Only one browser connection is served at the same time, and reconnection is allowed after disconnection;
- `SendResponse` is protected by a mutex and can be called from other threads;
- `Stop()` will cancel the I/O and wait for the listening thread to exit.

The protocol has no request identifier. A client must serialize requests on one
connection and read one response after each request; concurrent requests cannot
be correlated safely.

## Security Boundary

Named pipes should only be used between launcher and game processes on the same Windows host and should not host long-term credentials or server-side management keys. The current payload pipeline security descriptor allows any process on the local machine to connect, so the protocol parameters must still be regarded as untrusted input; if sensitive information is transmitted in the future, it should be changed to an ACL that limits the user SID, and message-level identity verification should be added.

When modifying the protocol, update the C++ dispatch logic, every consuming
client implementation, and this command table together.
