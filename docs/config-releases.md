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

## Deployed together

Sometimes Argo CD never syncs a digest bump's own commit: a human commit, or
another service's deploy commit, lands on top of it first, and Argo CD
applies the newer commit with the bump inside it (plan section 8.7, rule 2).

An application release whose GitOps commit was never synced adopts a later
sync when both hold:

- the sync started after the release's `gitops.updated`, allowing 30 seconds
  for Argo CD starting just before the event arrives;
- it is the first sync after that point to start new pods, and those pods
  carry the release's `release-id` annotation.

The release then takes that sync for Reconcile ("Synced `eeeeeee`, which
includes `2222222`") and that window for Rollout and Verify. A later rollout
that still carries the same annotation, such as a config change made after
it, never adopts the release.

If the newer commit is a human commit, its config release and the
application release are **deployed together**: each names the other in
`deployed_with`. They share the rollout, and the verifier checks only the
application release; the config release shows the same checks. If the newer
commit is another service's deploy commit, there is no config release and
only the application release records the sync.

An older bump that was skipped entirely, because a newer bump landed before
Argo CD synced, never ran: its pods were never started, so it stays waiting at
Reconcile.

## Hand edits

A config release is **flagged** when its new pods carry another `release-id`
annotation or digest than the pod template did before the sync, from the
last Deployment snapshot before the window. That is a hand edit to what runs,
such as a manual rollback, and it bypassed CI. The UI shows a HAND EDIT tag
and the reason, for example:

> A commit without a CI release moved the pods from
> `personal-site/c2cfca1/production/13` to `personal-site/ef8c431/production/12`

A config release deployed together with an application release is not
flagged, since the change came from CI's own bump. The release it moved the
pods to is not reopened.

## Not yet handled

- **A Deployment change that replaces no pods**, such as a replica count
  change alone, produces runtime events but no new ReplicaSet. Its Rollout
  stays running until the next sync ends it `unknown`.
