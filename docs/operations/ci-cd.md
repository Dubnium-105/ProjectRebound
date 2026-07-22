# ProjectRebound CI/CD usage and configuration

English | [简体中文](ci-cd.zh-CN.md)

This repository uses GitHub Actions, GitHub Container Registry (GHCR), GitHub Environments and native OpenSSH to complete CI/CD. The control plane and Edge Relay are two independent deployment targets that can be on different hosts and use different approvers and Secrets.

## 1. Workflow

### CI and Images

File:`.github/workflows/ci.yml`

Executed on every push, pull request to main and manual run:

1. Go format check and `go mod verify`;
2. `go vet ./...`;
3. `go test -race ./...` on PostgreSQL 17 service container;
4. Control Plane and Edge Relay binary build;
5. .NET 8 Shared Contracts build;
6. actionlint, Shell syntax and LF end-of-line checking;
7. Key generator, two copies of Compose and Caddy configuration verification;
8. After all quality checks pass, use Buildx once to build the control plane and Edge Relay images.

Normal branch, push of `main`, `v*` tag, and manual CI operation will directly publish the build as a deployable GHCR CI product:

```text
ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit>
ghcr.io/<owner>/projectrebound-edge-relay:sha-<40-char-commit>
```

Pull requests only build and verify, do not log in to GHCR, and do not publish images. `main` updates `:main` at the same time, and tag push releases the corresponding tag at the same time. Deployments always use the full SHA tag, not the removable `main` or `latest`. The image comes with OCI metadata and GitHub artifact provenance attestation.

The container will not be built first in a job and then again in the release job. After passing the quality check, the same matrix job is built once and directly pushes the immutable SHA image; CD reads the commit SHA of CI, sets `DEPLOY_SOURCE=ci` on the remote end and only executes `docker compose pull`, without recompiling the source code on the deployment host.

### Deploy

File:`.github/workflows/deploy.yml`

- After CI succeeds on `main` and images are published, the two staging targets are deployed automatically when repository variable `ENABLE_STAGING_DEPLOY=true`.
- `workflow_dispatch` can be manually selected from `staging`/`production` and `control-plane`/`edge-relay`/`both`.
- production should be approved by GitHub Environment Required Reviewers and have Prevent self-review enabled.
- Deployments in the same environment and target use concurrency serialization and will not cancel each other.
- The control plane automatically creates a PostgreSQL custom-format backup before deployment.
- Perform a health check after the deployment is completed; automatically try the previous release and the previous image if it fails.

## 2. GitHub repository settings

### 2.1 Actions permissions

Leave minimal default permissions at `Settings -> Actions -> General`. There is only `contents: read` at the top level of the workflow; only the container build/release job is temporarily obtained:

```yaml
packages: write
attestations: write
id-token: write
```

Do not create a PAT with repository-management permissions for image publication; use the job's automatically generated `GITHUB_TOKEN`.

### 2.2 Environments

Create four Environments:

```text
staging-control-plane
staging-edge-relay
production-control-plane
production-edge-relay
```

Recommended configuration for production environment:

- Required reviewers;
- Prevent self-review;
- Only main and protected `v*` tags are allowed;
- Prohibit administrators from bypassing protection rules;
- Store secrets only in the corresponding Environment, not at repository level.

### 2.3 Repository-level variables

| Name | Example | Description |
| --- | --- | --- |
| `ENABLE_STAGING_DEPLOY` | `false` |Set to `true` to enable main automatic deployment staging|

Keep `false` until first configuration is complete and manual deployment succeeds.

## 3. Configuration of each Environment

All targets set the following Variables:

| Variable | Example | Description |
| --- | --- | --- |
| `DEPLOY_HOST` | `192.0.2.10` |SSH DNS name or IPv4; workflow intentionally does not accept unnormalized input|
| `DEPLOY_PORT` | `22` |SSH port|
| `DEPLOY_USER` | `projectrebound` |Deploying users without interaction|
| `DEPLOY_ROOT` | `/opt/projectrebound-control` |release, current symlink and backup root directory|

Control Plane Environment Additional Variables:

| Variable |Example|
| --- | --- |
| `CONTROL_PLANE_ENV_FILE` | `/etc/projectrebound/control-plane.env` |
| `PUBLIC_BASE_URL` | `https://api.example.com` |
| `ENABLE_MONITORING` | `1` |

Edge Relay Environment additional variables:

| Variable |Example|
| --- | --- |
| `EDGE_RELAY_ENV_FILE` | `/etc/projectrebound/edge-relay.env` |
| `EDGE_RELAY_CONFIG_FILE` | `/etc/projectrebound/config.edge-relay.yaml` |

Add the following Secrets to each Environment:

| Secret |content|
| --- | --- |
| `SSH_PRIVATE_KEY` |Passwordless Ed25519 deploy key private key specific to this target|
| `SSH_KNOWN_HOSTS` |Target host key row checked through trusted out-of-band channels|
| `GHCR_USERNAME` |Read-only container account name|
| `GHCR_TOKEN` |Only the classic PAT of `read:packages` is used for remote Docker pull private images|

Do not run unverified `ssh-keyscan` in workflows. `SSH_KNOWN_HOSTS` must be verified from the console, cloud vendor fingerprint, or another trusted channel.

If the GHCR package is public, the script can be extended to allow anonymous pulls; authentication is currently forced by default to avoid misconfigurations being exposed mid-deployment.

## 4. Remote host preparation for the first time

For complete steps to install Docker and dependencies, see `docs/operations/deployment-guide.md`. Create separate users and directories for each target, for example:

```bash
sudo useradd --create-home --shell /bin/bash projectrebound
sudo install -d -o projectrebound -g projectrebound \
  /opt/projectrebound-control \
  /opt/projectrebound-control/releases \
  /opt/projectrebound-control/backups
sudo install -d -m 700 -o projectrebound -g projectrebound /etc/projectrebound
```

Add the deployment public key to `/home/projectrebound/.ssh/authorized_keys`. It is recommended to only allow VPN/bastion from the GitHub-hosted runner exit; when directly opening SSH on the public network, additional firewalls, Fail2ban and strict key-only login must be used.

The control plane host generates and edits the persistent configuration:

```bash
cd /path/to/checked-out/Backend
./scripts/generate-control-plane-env.sh /etc/projectrebound/control-plane.env
chmod 600 /etc/projectrebound/control-plane.env
```

Edge host preparation:

```bash
install -m 600 deployments/edge-relay/.env.example \
  /etc/projectrebound/edge-relay.env
install -m 600 deployments/edge-relay/config.edge-relay.yaml.example \
  /etc/projectrebound/config.edge-relay.yaml
```

Edit all placeholders. Set the Bootstrap Token when registering Edge for the first time; after the deployment script is successful, the token in the Edge env will be cleared and the identity volume will be verified by rebuilding.

The deploying user must be able to execute Docker. You can join the `docker` group, or just grant passwordless `sudo docker` like the test environment; the latter is still close to root privileges and should limit the SSH key and sudoers command scope.

## 5. First release and deployment

1. Push to the branch ready for deployment (the first production release is usually merged into main) and wait for `CI and Images` to be fully green.
2. Confirm that both `sha-<commit>` images exist on the repository's Packages page.
3. Keep `ENABLE_STAGING_DEPLOY=false`.
4. Open `Actions -> Deploy -> Run workflow`.
5. Select `staging` and a target; `commit_sha` is left blank to indicate the currently selected ref.
6. Verify control-plane and edge-relay respectively.
7. Then select `both` to do a complete staging release.
8. After successful verification, `ENABLE_STAGING_DEPLOY` can be set to `true`.

The Deploy workflow is the recommended entry point for production. If you really need to run the underlying script directly on the target machine, log in to GHCR first, and then explicitly specify the CI product:

```bash
DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh

DEPLOY_SOURCE=ci \
EDGE_RELAY_IMAGE=ghcr.io/<owner>/projectrebound-edge-relay:sha-<40-char-commit> \
  ./scripts/deploy-edge-relay.sh
```

The script defaults to `DEPLOY_SOURCE=auto`: legal GHCR SHA images are automatically pulled from CI, otherwise they fall back to source code construction. Production automation always explicitly uses `ci`, preventing accidental live builds on the server if the image variable is missing.

For production releases, it is recommended to create and push protected tags first:

```bash
git tag -s v1.0.0 -m "ProjectRebound v1.0.0"
git push origin v1.0.0
```

Wait for the tag image to be released successfully, then manually run Deploy, select `production` as the environment, and fill in the complete commit SHA corresponding to the tag. The approver checks the CI, image digest, database backup and change order before approving.

## 6. Remote release layout

```text
/opt/projectrebound-control/
  releases/
    sha-<commit>-<run>-<attempt>-control/
      Backend/
      .deployed-image
  current-control-plane -> releases/<active-release>
  backups/
```

Edge uses `current-edge-relay`. The deployment bundle does not contain `.env`, specific Edge YAML, `identity.json`, or backups. The GHCR Token is only sent to `docker login --password-stdin` through SSH stdin and will not appear in the remote command parameters or bundle.

The workflow does not automatically delete old releases. After confirming the backup and rollback window, operators may remove them by explicit path.

## 7. Rollback

When deployment fails, `remote-deploy.sh` automatically reads the `.deployed-image` of the previous release and attempts to recover. When automatic rollback also fails, the workflow will clearly report `ROLLBACK_FAILED` and requires manual processing.

Active rollback: Run Deploy manually, setting `commit_sha` to the old full SHA that still exists in GHCR. The control plane will still back up the database before switching. Application rollback cannot automatically undo database migration; database recovery that is not backward compatible must be performed according to the production incident process.

## 8. Branch protection suggestions

main requires at least the following checks:

```text
Go backend, PostgreSQL and contracts
.NET contracts
Deployment and workflow configuration
Build and package control-plane image
Build and package edge-relay image
```

Also enabled:

- Require pull request reviews;
- Require branches to be up to date;
- Require conversation resolution;
- Disable force push and branch deletion;
- Limit who can create the `v*` tag.

Dependabot checks GitHub Actions major tag updates weekly. When security requirements are higher, all third-party actions should be pinned from major tag to full commit SHA and updated via controlled Dependabot PR.

## 9. Fault location

- CI Go failure: first check the PostgreSQL service health, and then look at the specific package output.
- Compose/Caddy failed: The config command in the workflow is reproduced after running `Backend/scripts/generate-control-plane-env.sh`.
- GHCR push 403: Check the job's `packages: write` and package/repository associations.
- SSH host verification failed: Recheck the host key through the trusted channel, do not disable `StrictHostKeyChecking`.
- Remote pull denied: Check whether the GHCR account in Environment has `read:packages`.
- Control deploy failed: View workflow backup results, health checks, and `ROLLBACK_OK/FAILED`.
- Edge deploy failed: Check for 443 enrollment, 9090 mTLS and UDP advertised endpoints; do not delete the identity volume as a first reaction.

GitHub official reference:

- https://docs.github.com/actions/tutorials/publish-packages/publish-docker-images
- https://docs.github.com/actions/reference/workflows-and-actions/deployments-and-environments
- https://docs.github.com/actions/reference/security/secure-use
