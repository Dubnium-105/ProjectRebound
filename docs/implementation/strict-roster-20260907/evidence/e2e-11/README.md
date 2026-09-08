# E2E-11 stable lab evidence

These files are lab-only source/log/report evidence for the single isolated E2E-11 session. Credentials and Steam tickets are synthetic; no executable, target directory, or encrypted production configuration is included. The integrated driver input was built from Toolbox commit `32dcee8616cb6b87271a13b5165df335e54311a2`; later product commits are not retroactively claimed.

The integrated driver and killed config-writer used the same resolved `config::app_data_root()?.join("app_config.json")` path. The writer was killed after `sync_all()` and before replacement; the driver then called the real config loader and verified the expected epoch/token version and no pending file. `audit-final.log` records the source expression, writer environment path, flush boundary, and post-kill assertions. The report and all referenced logs are copied under this `docs/implementation/strict-roster-20260907/evidence/e2e-11` directory.
