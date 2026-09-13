# r14 candidate rejected before delivery

The r14 package was built and installed locally, then rejected during source review: a pre-listen NetDriver binding could be reported as authority-ready without requiring successful native `InitListen`.

The original ZIP and receipts are retained. ZIP SHA-256: `0eefdd95968142cf4dbde615188b147603dc397ea0332f27b7c9dd0109e583d1` (23,798,680 bytes). Passing component tests and package preflight did not cover this integration defect. No physical native multiplayer pass was claimed.

Use the [r14b record](../host-preflight-20260913-r14b/README.md) for the revised candidate and retained historical evidence. Do not distribute the r14 candidate.
