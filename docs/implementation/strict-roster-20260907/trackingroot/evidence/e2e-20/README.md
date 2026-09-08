# E2E-20 stable lab evidence

This directory contains the reproducible E2E-20 harness source, synthetic updater fixture, strict-gate driver source, and hashed logs/results. It contains no executable, Cargo target directory, production encrypted configuration, Steam ticket, or real access/refresh token. All candidate bytes and credentials used by the harness are synthetic.

The Toolbox source input was commit `22015fb41fa06fea0b8767049109291c89758fff`, with a clean tree. The ProjectRebound provenance input was commit `56a085c33d65a5775b6a5cd2177e14545d8b5321` and was dirty; the release provenance gate therefore returned its expected exit code 3. The hard-kill updater test deliberately records the product's orphaned `.rebound-toolbox-pending-*` behavior as PARTIAL.
