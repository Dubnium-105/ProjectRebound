# Draft: remove exposed database dump objects from GitHub

Recipient: GitHub Support, sensitive data removal request. **Not submitted.**

Repository: `Dubnium-105/ProjectRebound` (public).

An accidental documentation commit included four local PostgreSQL custom-format database dumps and local native test fixture source. The dumps contain TABLE DATA entries and were not intended for publication. This request does not attach the dumps or disclose any database row values.

First affected commit: `45e825f4e3799da460dfedecc9791b123698dffd`.

Affected paths:

- `docs/implementation/strict-roster-20260907/evidence/backend-schema47-source-20260907-180424.dump`
- `docs/implementation/strict-roster-20260907/evidence/backend-schema47-source-20260907-authority-185813.dump`
- `docs/implementation/strict-roster-20260907/evidence/backend-schema47-source-20260907-authority-final.dump`
- `docs/implementation/strict-roster-20260907/evidence/backend-schema47-source-20260907.dump`
- `Backend/.tmp-native-fixture/`

The affected branch `codex/authoritative-only-20260907` was reset from that accidental commit to its clean parent `8069d5e1126b8a585610b232e721aee45c57ee88` and force-pushed with an exact expected-old-SHA lease. The remote branch tip was then verified. No earlier clean commits were rewritten. Local files were retained outside version control.

The GitHub API query for pull requests associated with the affected commit returned an empty list (0). No LFS files were involved in the accidental commit. We have not established whether any third party fetched or cloned the data during exposure.

After the branch correction, the affected commit was still retrievable by its SHA through the GitHub Git commits API. Please remove the cached views/references and run server-side garbage collection for the accidentally published database dump objects, subject to your sensitive data removal procedure. Please advise if another reference prevents removal.

Deployment workflow run `34250303094` used the affected branch revision as its workflow ref, but its explicit deployed source input and all service images were the clean CI commit `8069d5e1126b8a585610b232e721aee45c57ee88`. The clean deployed source and test package do not include these files.

We have confirmed the dumps contain data sections but have not assessed individual row values in this draft; do not infer that the files are schema-only or that credential exposure has been ruled out. Database row contents, credentials and private dump files are intentionally omitted from this request.

Reference: https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository
