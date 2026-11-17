# Tracking mybuilds itself

mybuilds is the service `mybuilds` in its own service map, with the same
digest pinning, `managed` label, `release-id` annotation and `X-Release-ID`
header as every other service. Its release workflow reports
`release.published`, `gitops.superseded` and `gitops.updated` like
personal-site's, and its Argo CD Application subscribes to the same
notifications. See the plan, section 8.8.

What makes it different is that a deploy of mybuilds replaces the one pod
that is watching. The Deployment uses `Recreate`, because SQLite has one
writer, so for a short time nothing observes anything.

| Moment | What is recorded |
| --- | --- |
| Before the old pod stops | It sees the Deployment's new generation and records it. On `SIGTERM`, the watch records what is still queued before the database closes ([`runtime.md`](runtime.md), "Shutdown"). |
| The gap | Nothing is listening in the cluster, but the Worker is: GitHub, CI and Argo CD deliveries wait in the queue ([`buffer.md`](buffer.md)) and are drained once the new pod is up. |
| After the new pod starts | Its first list shows the finished state: the new ReplicaSet, its own pod and the Deployment's status. Rollout completes from those, and the pod steps are rebuilt from object timestamps and marked **reconstructed**. Pod readiness is seen live, since the pod lists before it is Ready. |
| Verify | The new pod checks its own public URL. The `X-Release-ID` match is real evidence, but a broken verifier could pass its own release, so the AWS probe stays the independent check. |

A failed self-deploy cannot be recorded: if the new version never starts,
no mybuilds is running to see it. When a fixed version starts, that release
shows Rollout `unknown`, and the outage is the AWS probe's to detect.

The first release after mybuilds starts tracking itself is partial: its CI
events and webhooks reach the previous version, which does not know the
service, so only what the new version observes is recorded.
