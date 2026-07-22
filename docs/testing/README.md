# Testing and acceptance

English | [简体中文](README.zh-CN.md)

ProjectRebound uses four test layers:

1. unit, race-detector, PostgreSQL/Redis integration, and contract checks;
2. a disposable real control plane with two Relay nodes;
3. weak-network, SIGKILL, dependency restart, and migration fault gates;
4. one-hour, six-hour, and 24-hour stability and capacity gates.

A continuous-online soak must not restart a healthy Relay. Relay migration, certificate recovery, and weak-network injection use separate scenarios so expected fault windows do not contaminate steady-state packet-loss measurements.

- [V1.1 acceptance index](v1.1/README.md)
- [Real container integration](../../Backend/tests/integration/README.md)
- [Load tests](../../Backend/tests/load/README.md)
- [V1.1 long-run harness](../../Backend/tests/load/longrun/README.md)
- [Weak-network tests](../../Backend/tests/netem/README.md)
- [Fault injection](../../Backend/tests/chaos/README.md)
