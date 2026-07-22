# SDK symbol search

English | [简体中文](README.zh-CN.md)

`find-loadout-symbols.ps1` scans generated C++ SDK files for weapon, role and inventory/loadout symbols. Generated reports are local research artifacts and are intentionally excluded from Git because they contain SDK-version-specific paths and line numbers.

From the repository root:

```powershell
pwsh -File Tools/SdkSearch/find-loadout-symbols.ps1
```

The default input is `Payload/SDK`; output is written to `Tools/SdkSearch/output/`. Both paths can be overridden:

```powershell
pwsh -File Tools/SdkSearch/find-loadout-symbols.ps1 `
  -SdkPath C:\path\to\SDK `
  -OutputDirectory C:\tmp\projectrebound-sdk-search
```
