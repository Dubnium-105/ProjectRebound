# Sanitized golden packets

English | [简体中文](README.zh-CN.md)

These samples contain no real Steam ID, access token, Gate Ticket, player
profile, or loadout. Native protocol files are lower-case hexadecimal without
the four-byte TCP length prefix. QoS includes the complete UDP datagram.

- `gate.hex`: redacted Gate handshake wrapper.
- `profile.hex`: player archive query wrapper.
- `party.hex`: Party creation wrapper.
- `matchmaking.hex`: matchmaking start wrapper.
- `qos.hex`: valid `0x59` QoS request with a three-byte echo payload.
- `login.json`: redacted authenticated Meta session request.

Captured packets must be sanitized and reviewed before replacing these files.
