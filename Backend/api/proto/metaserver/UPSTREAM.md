# BoundaryMetaServer protocol provenance

English | [简体中文](UPSTREAM.zh-CN.md)

- Repository: `https://github.com/Dubnium-105/BoundaryMetaServer`
- Branch: `master`
- Commit: `d68e717267abf14e32d4e39618f9b7680ed93046`
- Imported directories: `game/proto/Request`, `game/proto/Response`
- Imported file count: 41
- Per-file hashes: `UPSTREAM_MANIFEST.sha256`
- License: GNU Affero General Public License v3.0, matching Project Rebound.

The files under `upstream/` are a source mirror used for protocol review and
drift detection. Production code uses the generated static Go messages from
`metaserver.proto`; it does not parse `.proto` files at runtime.

`Response/matchmaking_ext.proto` labels its field numbers tentative. Those
fields are not part of the production matching state machine until a sanitized
real-client capture is reviewed and committed as a golden packet.
