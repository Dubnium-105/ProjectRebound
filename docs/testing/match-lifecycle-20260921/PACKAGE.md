# r14b lifecycle test candidate

English | [简体中文](PACKAGE.zh-CN.md)


This is a hardware-test candidate with `release_ready=false`. Three-account native gameplay and all four transport modes remain **NOT_RUN**.

The coordinator must first deploy the matching Backend source and apply migration **49**. Packaging did not deploy any service or change a database. The client still uses the existing api/cnapi/meta service origins. A schema 48 service is not a valid acceptance environment for this candidate. The Linux server package is an operator handoff without credentials.

Verify the archive SHA-256, extract every file, and exit Boundary and older Toolbox versions. Run `Install-StrictPayload.ps1 -GameWin64 '<game Win64 directory>'`, then `Check-ThisMachine.ps1` with the same directory. Start `Run-Toolbox.cmd`, use separate legitimate Steam accounts, and create a fresh lobby. The installer backs up the old DLL; launching Toolbox alone does not install the bundled Payload.

The required game EXE SHA-256 is `181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`. Existing loader/data files, WebView2 and the manifest's minimum x64 VC++ runtime are required. The game itself is not included.

Follow `TEST-MATRIX.zh-CN.md`. ENDING preserves the native result/return window. Cleanup failures retain a retry action; both local processes and Backend transports must be released before the next lobby. Existing native admission gates remain in force. Record BLOCKED where appropriate; do not change capability flags or bypass admission with console commands.

For rollback, exit game and Toolbox, then run `Restore-StrictPayload.ps1 -ReceiptPath '<installation receipt>'`. Structured collection is available through `Check-ThisMachine.ps1 -Collect`; do not share account configuration, tokens, raw logs, DPAPI recovery files or backups.

Both Windows binaries carry the existing test signature. No trusted root is installed and no private key is shipped. Hash/signature checks do not prove native gameplay or production release acceptance.
