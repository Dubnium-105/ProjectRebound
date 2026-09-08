# E2E-20 stable lab evidence

`e2e-20-result-pre-journal-fix.json` preserves the original PARTIAL run, including the orphan pending file observed after a hard kill. The `*-journal-fix-precommit` files preserve the subsequent dirty-tree validation and remain historical.

The canonical current report is `e2e-20-result.json`; `e2e-20-result-final-cd09.json` is the same report retained with its committed Toolbox source label. The current run used Toolbox commit `cd09d7e7f769dc73e26ed7f38233b0a0a605e273` with a clean tree and ProjectRebound commit `85073009a7865779135fa7a4cc4f86fffa284862` with tracked source changes. The harness exited 0 and recorded 15 runnable steps. The report remains PARTIAL because the strict provenance gate returned its expected exit code 3 with five release-readiness blockers; this is not reported as release PASS.

The evidence uses the current repository fixture under `Tools/Release`; the old `.tmp/source-a049d56` fixture is not used for current claims. The directory contains reproducible harness/fixture/strict-gate source and hashed logs/results, but no executable, Cargo target directory, production encrypted configuration, Steam ticket, or real access/refresh token. All candidate bytes and credentials used by the harness are synthetic.
