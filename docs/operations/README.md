# Operations documentation

English | [简体中文](README.zh-CN.md)

## Deployment and release

- [Deployment entry point](deployment.md): responsibilities and order for the three host roles;
- [Complete Debian deployment guide](deployment-guide.md): hosts, ports, firewall, FRP/HAProxy, and enrollment;
- [CI/CD](ci-cd.md): GitHub Actions, immutable GHCR artifacts, and environments;
- [Release and rollback](release-and-rollback.md): control-plane release and one-at-a-time Relay rollout.
- [MetaServer deployment](metaserver-deployment.md): separate control host, public gateway, Relay, client, and rollback checklist;
- [Dedicated Server registration](dedicated-server-registration.md): invitation/grant verification, one-time enrollment, Windows Agent, and rotating node identity;
- [Admin Web operator guide](admin-console-user-guide.md): sign-in, player, online, release, governance, and audit workflows;
- [Admin Web security guide](admin-console-security.md): access layers, secret boundaries, administrator lifecycle, and security checks.
- [Download catalog and local MinIO](download-catalog.md): default self-hosted storage, browser CORS, upload verification, backup, and recovery.

## Stability and data safety

- [Relay continuity and recovery](relay-continuity.md): no scheduled restarts, offline recovery, certificate renewal, and monitoring;
- [Key and certificate rotation](key-and-certificate-rotation.md): Relay token keyset, node certificates, and revocation;
- [Backup and restore](backup-and-restore.md): PostgreSQL, offline keys, and recovery order;
- [Incident runbooks](runbooks/README.md): production response by alert type.
