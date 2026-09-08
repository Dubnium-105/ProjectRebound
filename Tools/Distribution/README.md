English | [简体中文](README.zh-CN.md)

# Building a hardware-test package

This directory prepares test-distribution materials for a strict-roster candidate whose native acceptance is not complete. It does not change production release gates or treat a successful package build as acceptance of an online match.

`build_test_packages.py` receives an already reviewed Windows staging directory and public metadata JSON. A server artifact handoff can be supplied with `--server-stage`. Every binary must be listed explicitly in `required_artifacts` with its path, byte count, and SHA-256. The script rejects private keys, database dumps, credential configuration, and extra binaries, writes a per-file inventory for every package, and rereads ZIP/tar.gz archives to compare their file sets and original bytes.

```text
python build_test_packages.py --windows-stage <windows-staging> \
  --metadata <public-metadata.json> \
  --output <new-output-directory>
```

The output directory must not exist. Windows packages require testers to install a legitimate fixed-version Boundary game themselves; the game executable, Steam sessions, and developer-machine configuration are not distributed. This release uses the existing api/cnapi/meta services directly. The normal client is not compiled with `lab-testing`, and no separate test backend is configured. If server artifacts are handed off, they are only for a coordinator updating the existing service and cannot be used as a normal client installation package. Packages contain no existing account, private key, or access credential.

Metadata must contain `schema_version=1`, `purpose=hardware-test-only`, `release_ready=false`, `package_id`, the actual `source_commits`, `pinned_game`, and `required_artifacts` entries for `windows-x64` and `server-linux-amd64`. Each package records its role and the hashes of all ordinary files; `package-manifest.json` is covered by `SHA256SUMS`, and the latter is covered by the outer ZIP/tar.gz hash.

Installation and restore scripts are in `windows-install`; check and collection scripts are in `windows-native`. `sign_test_artifact.ps1` signs a new copy with the existing code-signing certificate, checks the same WinVerifyTrust result used by the product, and never exports a private key or changes the trust store. Sign Payload first, compile the strict Toolbox with the signed Payload SHA, and sign the Toolbox last. Record the actual build command, source commits, execution results, and distribution-file summary in that test-build record; keep the historical acceptance ledger unchanged.
