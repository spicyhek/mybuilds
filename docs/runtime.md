# Runtime watch and the Rollout stage

mybuilds watches the Kubernetes objects of each service's workload and turns
what it sees into stored events, the same way it stores webhooks. The Rollout
stage is projected from those events. See the plan, sections 8.4 and 8.7.

## What is watched

`internal/kube` runs one informer each for Deployments, ReplicaSets and Pods
in every namespace named by the service map's `workload` entries, filtered by
the label `mybuilds.brendanmanley.com/managed=true`:

```json
"workload": { "namespace": "default", "deployment": "personal-site" }
```

- **Read-only.** The ServiceAccount has `get`, `list` and `watch` on those
  three resources and nothing else. Without it, the app runs normally and its
  releases stop at Reconcile.
- **Light on the API server.** Informers list once and then watch. They
  resync every 30 minutes from their own cache, and managed fields are dropped
  from cached objects.
- **Allowlisted fields.** Each object becomes a snapshot of the fields the
  Rollout stage needs: replica counts, generations, revisions, conditions
  without their messages, pod readiness, image digests, restart counts and
  waiting reasons. Node names, pod IPs, environment variables and volumes are
  never copied.
- **Stored once.** An event's delivery ID is the object's UID plus a hash of
  its snapshot. A resync, or the initial list after a restart, sees unchanged
  objects and stores nothing; the initial list also records anything that
  changed while mybuilds was down. Each snapshot carries the object's
  generation, so an object that changes and then changes back, such as a
  ReplicaSet scaled to zero and up again, is not mistaken for one already
  stored.

| Event type | Workload key | Snapshot |
| --- | --- | --- |
| `k8s.deployment` | `namespace/name` | revision, generation and observed generation, desired, updated, ready and available replicas, Progressing and Available conditions |
| `k8s.replicaset` | `namespace/<owning Deployment>` | revision, pod-template hash, creation time, generation, replica counts |
| `k8s.pod` | `namespace/<Deployment of its ReplicaSet>` | pod-template hash, phase, Ready and its transition time, per container: image, `imageID` digest, restarts, state and reason |

## Attribution

A rollout is attributed through Argo CD's sync revision, never through the
pod template's `release-id` annotation (plan section 8.7, rule 1):

1. A release's GitOps commit is the revision of one of its Application's
   syncs.
2. The release owns the workload's runtime events from the start of that sync
   until the start of the Application's next sync of another revision. While
   no later sync exists, the window is open.
3. The first terminal outcome in the window is final. If the next release
   starts rolling out before its sync notification arrives, those events fall
   into the open window for a moment, but they cannot reopen a rollout that
   already succeeded or failed. When the notification arrives, the window
   closes and both releases are re-projected.

A sync of a commit no CI run made belongs to a config release, which owns
the window the same way. See [`config-releases.md`](config-releases.md).

## Rollout rules

Within the window, the new ReplicaSet is the one at the Deployment's
`deployment.kubernetes.io/revision`, as `kubectl rollout status` finds it.

| Outcome | When |
| --- | --- |
| succeeded | The Deployment's observed generation is current; desired, updated, available and total replicas are equal; and every Ready pod of the new ReplicaSet runs the release digest. Completion time is the Progressing condition's `NewReplicaSetAvailable` update time. |
| failed | Progressing is `False` with `ProgressDeadlineExceeded`; a container of a new pod has restarted 3 times; or a Ready new pod runs another digest. |
| succeeded, image by tag | A config release whose template names the image by tag, as before a service's first digest-pinned release, has no digest to expect. The first Ready new pod's digest is taken, every other Ready new pod must run the same one, and the Digest check step says so. Pods of one tag on different digests, such as a tag re-pushed mid-rollout, fail. |
| succeeded, scaled to zero | The Deployment wants no replicas, its observed generation is current and no pods are left. There is no digest to check. A config release's summary then names no serving release. |
| running | Evidence exists and neither of the above holds yet. |
| unknown | The window closed with no evidence, or before the rollout finished. |

A failed Argo CD sync applied nothing, so Rollout is skipped.

## Steps

| Step | Evidence |
| --- | --- |
| New ReplicaSet | Its name, revision and creation time |
| Pod `<name>` | Created to Ready, with the running digest and restarts; while waiting, the reason, such as `ContainerCreating`, `ImagePullBackOff` or `CrashLoopBackOff` |
| Old pods scaled down | The Deployment's total replicas reaching its updated replicas |
| Digest check | Every Ready new pod's `imageID` against the release digest |

### Reconstructed steps

Step times come from the objects, not from when the watch saw them: a
ReplicaSet's and a pod's `creationTimestamp`, and the Ready condition's
`lastTransitionTime`. So a rollout the watch only saw after the fact, from
its initial list, still has the right times. Its steps are marked
**reconstructed**, and the UI labels them so: a step is reconstructed when
the watch first observed the object, or first saw the pod Ready, more than
15 seconds after it happened. The "Old pods scaled down" step is
reconstructed when the new ReplicaSet's creation was missed, since the
scale-down around it was missed too. mybuilds' own rollouts always look
like this (see [`self-tracking.md`](self-tracking.md)).

## Shutdown

On `SIGTERM`, the watch stops its informers and records what is still
queued, for up to 5 seconds, before the database closes. A pod that its own
release replaces sees its Deployment's new generation just before it is
stopped, so the next version finds that the rollout started.

## Metrics

| Metric | Meaning |
| --- | --- |
| `mybuilds_runtime_events_total{kind, result}` | Observed objects by kind and result: `stored`, `duplicate`, `ignored` or `error` |
| `mybuilds_runtime_watch_synced` | 1 once the informers have completed their initial list |
