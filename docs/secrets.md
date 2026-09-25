# Secrets

**Project → Secrets** holds the values pipeline jobs need, such as tokens, passwords and keys.
Secrets are **write-only**: after you save a value, nobody can read it back through the UI or
the API, not even an Owner. Only a runner executing a job that lists the secret receives it.

## Scope and names

- A secret applies to **all environments** of the project, or to **one environment**. When both
  exist with the same name, a job with that environment gets the environment's secret; other
  jobs get the all-environments one.
- Names become environment variables: capital letters, digits and `_`, not starting with a
  digit, at most 128 characters (`DB_URL`, `NPM_TOKEN`). Names starting with `OPSHUB_` are
  reserved. A name is unique per scope.
- Values are text up to 64 KiB (no NUL bytes); multi-line values (keys, certificates) work.
- At most 500 secrets per project.

## Who can do what

| | Owner, Admin | Developer | Viewer |
|---|:-:|:-:|:-:|
| See names and history (`secret.list`) | ✅ | ✅ | — |
| Add, rotate, edit, delete: unprotected environment | ✅ | ✅ | — |
| Add, rotate, edit, delete: protected environment or all environments | ✅ | — | — |
| Read a value | — | — | — |

A secret for all environments also reaches protected ones, so Developers can't change it.
Every change is audited (`secret.create`, `secret.update`, `secret.rotate`, `secret.delete`),
with the name and scope but never the value.

## Using a secret in a pipeline

List the secrets a job needs under `secrets:` in [.opshub.yml](pipelines.md#jobs):

```yaml
jobs:
  publish:
    stage: release
    image: node:22
    environment: staging
    secrets: [NPM_TOKEN, DATABASE_URL]
    steps: [npm publish]
```

- The job receives only the secrets it lists, as environment variables. A secret wins over a
  variable with the same name.
- When the job becomes ready, OpsHub checks each name. A name with no secret fails the job
  (`secret_not_found`); its log names what's missing.
- **Pull-request runs never receive secrets** (`secrets_not_allowed`): their pipeline file comes
  from the pull request, so anyone who can open one could otherwise print the values. Push, tag,
  scheduled and manual runs receive them.
- Deploy jobs (`deploy:`) can't list secrets: OpsHub performs those deployments itself.
- A runner receives the values when it claims the job. Each value read writes an audit entry
  `secret.read` (actor: the runner, with the run, job and version).
- Values are masked as `••••••` in the job log, both by the runner and by OpsHub before it
  stores output. Each line of a multi-line value is masked too. Values shorter than 4
  characters aren't masked. Masking is a safety net: don't print secrets on purpose.

## Rotation and history

**Rotate** stores a new value as the next version and **destroys every older value**. Jobs
claimed from then on get the new value; running jobs keep the one they received. The history
keeps who set each version and when. Deleting a secret destroys its value too; jobs that list
it fail until a secret with that name exists again.

## Encryption

Each version is sealed with its own random 256-bit data key (AES-256-GCM). The data key is
sealed by the active master key from `OPSHUB_MASTER_KEYS` (envelope encryption). Both layers
are bound to the secret and version, so a value can't be moved to another row.

### Rotating the master key

1. Generate a key: `opshub-api keys generate` (use its `OPSHUB_MASTER_KEYS` line).
2. Put the new key **first** and keep the old one after it: `OPSHUB_MASTER_KEYS=m2:…,m1:…`.
   Restart OpsHub. New values now use the new key; old ones stay readable.
3. Run `opshub-api keys rotate`. It re-encrypts everything stored under older keys:
   - secret data keys (the values themselves stay as they are);
   - 2FA seeds;
   - Git access tokens and webhook secrets;
   - deploy-target credentials;
   - the log masks of running jobs.

   It is safe while OpsHub runs, skips what is already on the new key, and can be run again
   after an interruption. It prints counts per column.
4. When it reports **0 failed**, remove the old key and restart. Otherwise keep the old key:
   the failed rows could not be decrypted with any configured key. (A single sign-on login in
   progress while the key is removed has to be started again.)

Back up `OPSHUB_MASTER_KEYS` separately from the database. Without it, secrets can't be
recovered.

## API

See [api.md §8](api.md#8-secrets-module-8). No response contains a value. Runners receive
`secrets` and `masks` with an [assigned job](runners.md).
