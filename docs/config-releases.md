# Config releases

homelab-infra gets two kinds of commits to a managed app: digest bumps made
by CI, and human edits such as a resource limit or an environment variable.
A human edit that changes the pod template starts a rollout whose pods carry
the **previous** release's digest and `release-id` annotation. mybuilds
records that rollout as a config release of its own, so the previous
application release never changes. See the plan, section 8.7.

## Telling the commits apart

Argo CD's notifications include the synced commit's message and author
(`repo.GetCommitMetadata`, see [`ingestion.md`](ingestion.md)). Only the
subject line, the `Release-Id:` trailer and the author are kept.

When a sync notification arrives for a service's own Application:

| Commit | Release |
| --- | --- |
| Has a `Release-Id:` trailer (made by the bump job) | The application release that reported the commit in `gitops.updated` |
| No trailer | The config release `<service>/config-<short GitOps SHA>/<environment>` |
| No commit metadata at all | None: it cannot be told apart from a CI commit |

The author is never used to decide: a squash merge is attributed to the
token's owner, the same account that makes human edits. Health notifications
and the root app-of-apps never name a release.

The key is assigned when the notification is stored, from the notification
alone, so a rebuild cannot move a sync from one release to another.

## Stages

A config release uses the config template: GitOps commit, Reconcile,
Rollout, Verify.

| Stage | Evidence |
| --- | --- |
| GitOps commit | The commit's subject and author from Argo CD; started at the first sync notification |
| Reconcile | The Application's sync of the commit, as for application releases |
| Rollout | The workload between this sync and the next ([`runtime.md`](runtime.md)). The pods must run the digest of the new ReplicaSet's template image, and the release records the `release-id` annotation they carry |
| Verify | Five consecutive public checks ([`verification.md`](verification.md)) that serve the **annotated** release's `X-Release-ID`, since the pods still run that release |

The previous application release keeps its own Rollout and Verify: the config
commit's sync closes its runtime window, and a finished rollout never reopens.

### A sync that replaces no pods

A change to a Service or a ConfigMap starts no rollout, and the runtime watch
sees nothing. One minute after a config release's sync succeeds with no
runtime evidence, the verifier records `verifier.no_rollout`, and Rollout and
Verify are skipped. It only does so when the runtime watch had completed its
initial list before the sync started. Otherwise a watch that was down could
not tell "nothing happened" from "nothing was seen", and Rollout stays
pending until the next sync, which ends it `unknown`.

### Without a release-id annotation

If the new pods carry no `mybuilds.brendanmanley.com/release-id` annotation,
there is no `X-Release-ID` to expect, and Verify is skipped.

## Not yet handled

- **Deployed together (rule 2).** A human commit that lands on top of a
  digest bump before Argo CD syncs means the bump's own commit is never
  synced. The config release for the human commit records the rollout, which
  carries the application release's annotation and digest, but the
  application release stays waiting at Reconcile. Linking the two with
  `deployed_with` comes next.
- **Edits to release annotations.** A human commit that changes a
  `mybuilds.brendanmanley.com/` annotation is recorded as a config release,
  but not yet flagged as bypassing CI.
- **A Deployment change that replaces no pods**, such as a replica count
  change alone, produces runtime events but no new ReplicaSet. Its Rollout
  stays running until the next sync ends it `unknown`.
