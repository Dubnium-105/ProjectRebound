# Publication follow-up — 2026-09-09

English | [简体中文](publication-followup-20260909.zh-CN.md)

The GitHub sensitive-data removal request was submitted as [ticket #4739985](https://support.github.com/ticket/personal/0/4739985), whose state was `open` at submission. No database files, row values or credentials were attached. GitHub cache removal and historical-object deletion remain awaiting a response; the earlier unsent draft and correction receipt are retained as historical records.

The main repository's r7 documentation commit `f3a291dedb9fca3d56247a06770b87f2ac7aae8d` was pushed normally. The Toolbox's original test commit `43a4c82fe781ed67df6b4f4f82d1ff976ad586be` remains local; its history included temporary build output, so that history was not pushed directly.

The Toolbox was published from remote clean base `63e5ee598b860b8f5d53dd147d436921ad290098` as [clean commit `f69d567`](https://github.com/STanJK/ProjectReboundToolbox/commit/f69d56741208263c972309137e462a251b3b0eba) on branch `codex/authoritative-only-20260907`. It included 109 source and required-resource paths; each Git blob was checked against the original test commit, with 10,833 temporary or build paths excluded. The embedded MetaTunnel runtime was a required resource, and its manifest size and SHA were checked. Synthetic credential examples used by tests were retained; no real runtime credentials were found.

The r7 test-package bytes and backend deployment are unchanged. This update only organizes the public commits and records the request state; it does not rebuild or claim new physical-machine acceptance. Native admission on three machines and a complete playable match remain unverified.

See the [status receipt](publication-followup-20260909.json). Continue collaboration from the clean public commit above and do not merge the old local history back into the public branch.
