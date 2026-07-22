# SDK符号搜索

[English](README.md) | 简体中文


`find-loadout-symbols.ps1`扫描生成的 C++ SDK 文件中的武器、角色和库存/装载符号。生成的报告是本地研究工件，有意从 Git 中排除，因为它们包含 SDK 版本特定的路径和行号。

从存储库根目录：

```powershell
pwsh -File Tools/SdkSearch/find-loadout-symbols.ps1
```

默认输入是`Payload/SDK`;输出被写入`Tools/SdkSearch/output/`。两条路径都可以被覆盖：

```powershell
pwsh -File Tools/SdkSearch/find-loadout-symbols.ps1 `
  -SdkPath C:\path\to\SDK `
  -OutputDirectory C:\tmp\projectrebound-sdk-search
```
