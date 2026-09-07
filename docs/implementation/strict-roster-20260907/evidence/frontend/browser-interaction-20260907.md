# Actual browser interaction record — frontend only

Observed on 2026-09-07 around 22:16–22:19 Asia/Hong_Kong using `mcp__cua_repl.js` and the Codex in-app browser. Source: Toolbox commit `22015fb41fa06fea0b8767049109291c89758fff`; no product edits during this check.

Server invocation: `npm --prefix frontend run dev -- --host 127.0.0.1 --port 57431 --strictPort`. Vite 6.4.2 reported ready at `http://127.0.0.1:57431/`. The owned server was stopped with Ctrl-C after inspection (process exit 1 reflects deliberate interruption, not a failed acceptance assertion); the test tab was closed and no listener remained on 57431.

The following are observations from returned accessibility trees, not a replay script or a native test log:

1. Opened `http://127.0.0.1:57431/?tauri-mock=1`. Page showed `Mock Operator`, one `权威对局大厅`, a PvE launch button, and the lobby create/join controls. There was no arbitrary-address online Join field.
2. Clicked 创建房间. The form exposed 房间名称, 区域, 模式, 权威类型 (P2P/Dedicated), 传输线路 (Relay/VNT), and 最大人数. The submit button was disabled with an empty name. Entered `Roster UI verification`; submission became enabled.
3. Submitted. The Mock created `mock-lobby-02`; displayed TEAM 1 = 1/5, TEAM 2 = 0/5, and disabled `冻结清单并启动`. Creating/joining another lobby and PvE launch were also disabled while attached. The displayed process remained 未运行. This proves only UI handling of a synthetic roster, not backend creation or native admission.
4. Expanded Debug Log. It explicitly showed `Browser preview: explicit Tauri Mock active`, `Authoritative roster protocol: available`, `create_match_lobby requested`, and `create_match_lobby resolved and owner ready: mock-lobby-02 revision=2`.
5. Left the lobby. The authority panel disappeared and create/join/PvE controls became enabled. Joined `mock-lobby-01`; the UI showed the MEMBER roster, waiting for host freeze, and disabled duplicate join/other launch. This did not launch a game.
6. Reloaded into `http://127.0.0.1:57431/?tauri-mock=1&mock-fail=create_match_lobby`. Submitted `Expected create failure`. The form stayed open, showed `Mock create_match_lobby failed`, and the lobby list stayed at one room. No created-success state appeared.
7. Opened `http://127.0.0.1:57431/` with no Mock parameter. The page showed `LOGIN STEAM`, unavailable process/version/port status, zero rooms, and disabled create/refresh/PvE. Mock capabilities were not silently supplied.

Artifact-export limitation: `uiTab.content.export()` was attempted once and returned `Codex in-app browser does not support command "tab_content_export"`. No exported page file or screenshot artifact is claimed. This record preserves the actual observed text and tool sequence; it is not a browser-generated export.

Full packaged Tauri-to-Rust-to-backend lobby/operation execution remains NOT_RUN for E2E-18. The retired EGUI online entry was reviewed in `src/pages/launch.rs`; `docs/BUILD_TEST_RUN.md` declares only Tauri remains an online release, while `docs/BUILD_DEPLOY.md` explicitly marks the old guide as historical. Neither source review nor Mock interaction is promoted to native E2E PASS.
