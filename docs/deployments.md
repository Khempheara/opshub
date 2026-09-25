# Deployments

A deployment puts one **container image** (the *version*, e.g. `ghcr.io/acme/api:1.4.2`) on a
**deploy target** for a project's **environment**. OpsHub performs it from a background worker:
it applies the release, runs health checks, reverts automatically when the release is unhealthy,
and keeps the full release history with one-click rollback.

## Deploy targets

Targets belong to the organization (**Organization → Deploy targets**). Everyone in the
organization can see them; Owners and Admins manage them. Pipelines refer to targets by name.
Credentials are encrypted at rest and **write-only**: after saving, the UI and API only show
which credential fields are set.

| Kind | Where it deploys | Credentials |
|---|---|---|
| **SSH hosts** | Runs your deploy command on each host | Private key (+ passphrase) or password |
| **Docker host** | Replaces a container (or N replicas) with the new image, over SSH (the host's socket), TCP with TLS, or this server's own socket | SSH key, or CA + client certificate; optional registry login |
| **Kubernetes** | Updates a Deployment's image, or blue/green between `<deployment>-blue` and `-green` | kubeconfig with a service-account token or embedded client certificate |

**Test connection** checks the target without changing it. For SSH (and Docker over SSH) it
shows each host's key fingerprint: compare it with the server's administrator, then **Trust
this key**. OpsHub refuses hosts whose key isn't trusted or has changed, and sends no
credentials to them.

**Network access.** OpsHub blocks connections to private and internal addresses (SSRF
protection). List your targets' networks in `OPSHUB_OUTBOUND_ALLOWED_CIDRS` (e.g.
`10.0.0.0/8`). Health check URLs follow the same rule. Docker targets on the API host's own
socket are disabled unless the operator sets `OPSHUB_DEPLOY_LOCAL_DOCKER=true`; that socket is
root-equivalent on the host.

**Kubernetes credentials.** Kubeconfigs from EKS/GKE/AKS usually authenticate through exec
plugins, which OpsHub doesn't run. Create a service account with rights on the target's
Deployments (and Service, for blue/green) in its namespace, and paste a kubeconfig with its
token:

```bash
kubectl -n payments create serviceaccount opshub
kubectl -n payments create role opshub-deployer --verb=get,patch,create --resource=deployments,services
kubectl -n payments create rolebinding opshub-deployer --role=opshub-deployer --serviceaccount=payments:opshub
kubectl -n payments create token opshub --duration=8760h
```

## How each target deploys

**SSH (rolling).** Hosts are updated `batch_size` at a time. The command runs with `set -e`
and these variables: `OPSHUB_VERSION` (the image), `OPSHUB_PREVIOUS_VERSION`,
`OPSHUB_ENVIRONMENT`, `OPSHUB_PROJECT`, `OPSHUB_DEPLOYMENT_ID`, `OPSHUB_STRATEGY`. For example:

```bash
docker pull "$OPSHUB_VERSION" && docker rm -f web || true && docker run -d --name web -p 80:8080 "$OPSHUB_VERSION"
```

When a host's command fails or its health check fails, every host touched so far runs the
command again with the previous version (when there is one: the environment's current
release).

**Docker (rolling).** OpsHub pulls the image, then for each replica stops the running container,
keeps it as `<name>-previous`, and starts the new one with the target's ports, environment,
network and restart policy. A replica must keep running (and pass the image's `HEALTHCHECK`,
if any, and the target's health check URL). On failure the new containers are removed and the
previous ones restarted; on success the previous ones are removed. Published ports need exactly
one replica (two containers can't bind the same port).

**Kubernetes rolling.** OpsHub patches the container image (and an `opshub.io/deployment`
annotation, so redeploying the same image also rolls) and waits until every replica runs the
new template, up to `rollout_timeout_seconds` or the Deployment's progress deadline. On failure
it patches the previous image back.

**Kubernetes blue/green** (needs `service`). Two Deployments, `<deployment>-blue` and
`<deployment>-green`, carry the label `opshub.io/color`. OpsHub updates the idle color (creating
it from the live one — or from `<deployment>` the first time), waits until it is ready, then
switches the Service's selector to it. The previous color keeps running its image, so a
rollback is fast: that color is already ready when the selector switches back to it. After the
first blue/green deploy, `<deployment>` no longer receives traffic: scale it down when you're
ready. If the health check fails after the switch, the selector switches back.

**Health checks.** An optional HTTP URL per target (`{host}` becomes each SSH/Docker host's
name) must answer 2xx (or `expected_status`) within `timeout_seconds` (default 60). Results are
stored with the deployment.

## Starting deployments

- **Deploy button** (project → Deployments; Developers and up): environment, target, image,
  strategy.
- **Pipeline job** with a `deploy:` block ([pipelines.md](pipelines.md)): OpsHub creates the
  deployment when the job becomes ready (after its needs and any approvals) — no runner is
  involved. The job's log shows the deployment's output; the job succeeds or fails with it.

```yaml
deploy-staging:
  stage: deploy
  environment: staging
  deploy:
    target: k8s-prod
    strategy: rolling
    version: ghcr.io/acme/api:${OPSHUB_COMMIT_SHA}
```

**Protected environments.** Manual deploys and rollbacks need a role the environment allows.
Environments that require approvals accept deployments only from pipelines (where the approval
gate runs); rollback stays available to allowed roles for emergencies.

One deployment runs per environment at a time; a second one is refused
(`DEPLOYMENT_IN_PROGRESS`), and a pipeline deploy job started meanwhile fails with a message to
retry it.

## History and rollback

The Deployments tab shows what each environment runs now and the full history (status, image,
target, who or which pipeline run started it). **Roll back** on the environment's current
release redeploys the release that succeeded before it, as a new deployment linked to the one
it replaces. Rollback of anything else is refused (`NOTHING_TO_ROLL_BACK`).

Failure reasons: `deploy_failed`, `health_check_failed`, `target_unreachable`,
`host_key_untrusted`, `target_missing` (the target was deleted), `interrupted` (OpsHub
restarted mid-deployment — the target's state is unknown, so the deployment isn't retried),
`strategy_not_supported`. `reverted` tells whether the previous release was put back.

Every target change and every deployment start, rollback and result is in the audit log.
