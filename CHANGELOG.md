# Changelog

mybuilds follows [Semantic Versioning](https://semver.org/); README.md (Versioning) says when to bump which part.
1.11.0 is the first version mybuilds reports. The versions before it were assigned afterwards, by applying the same
rules to the merged pull requests in order, and are not tagged. Until 1.11.0 the build reported its image tag
(`sha-...`) as its version.

## 1.11.0 (2026-11-22)

- mybuilds reports a semantic version from `VERSION` in `/api/v1/meta`, the `mybuilds_build_info` metric, its
  User-Agent and the UI header, instead of the image tag.

## 1.10.1 (2026-11-22)

- Fix the Build and Publish stages' in-progress state, and animate GitOps and Reconcile while they wait.
  ([#16](https://github.com/spicyhek/mybuilds/pull/16))

## 1.10.0 (2026-11-19)

- The Verify stage tolerates a single failed check, and the GitHub jobs API is polled for more accurate job timing.
  ([#15](https://github.com/spicyhek/mybuilds/pull/15))

## 1.9.1 (2026-11-18)

- Fix rollout tracking for mynids releases that deploy by image tag. ([#14](https://github.com/spicyhek/mybuilds/pull/14))

## 1.9.0 (2026-11-18)

- Track mynids releases. ([#13](https://github.com/spicyhek/mybuilds/pull/13))

## 1.8.1 (2026-11-17)

- Fix releases marked unknown when ReplicaSet snapshots were missing.
  ([#12](https://github.com/spicyhek/mybuilds/pull/12))

## 1.8.0 (2026-11-17)

- Recorded events: each release's normalized events, in order, for replaying it as a test fixture.
  ([#11](https://github.com/spicyhek/mybuilds/pull/11))

## 1.7.1 (2026-11-17)

- Fix a reverted mybuilds config release stuck with its rollout running.
  ([#10](https://github.com/spicyhek/mybuilds/pull/10))

## 1.7.0 (2026-11-17)

- An off-cluster ingestion buffer (Cloudflare Worker and queue) and tracking of mybuilds' own releases. Direct
  delivery to mybuilds keeps working. ([#9](https://github.com/spicyhek/mybuilds/pull/9))

## 1.6.2 (2026-11-16)

- Fix the default replay behavior. ([#8](https://github.com/spicyhek/mybuilds/pull/8))

## 1.6.1 (2026-11-16)

- Replay starts only when a release is clicked, and long text wraps.
  ([#7](https://github.com/spicyhek/mybuilds/pull/7))

## 1.6.0 (2026-11-16)

- Replay past releases, with a timeline endpoint. ([#6](https://github.com/spicyhek/mybuilds/pull/6))

## 1.5.0 (2026-11-14)

- Release history screen, with a shareable URL for every release. ([#5](https://github.com/spicyhek/mybuilds/pull/5))

## 1.4.0 (2026-11-14)

- Releases deployed together, and hand edits flagged. ([#4](https://github.com/spicyhek/mybuilds/pull/4))

## 1.3.0 (2026-11-13)

- Track config releases from homelab-infra. ([#3](https://github.com/spicyhek/mybuilds/pull/3))

## 1.2.0 (2026-11-13)

- The Verify stage. ([#2](https://github.com/spicyhek/mybuilds/pull/2))

## 1.1.0 (2026-11-12)

- Kubernetes rollout tracking, and a runtime watch for stored events.
  ([#1](https://github.com/spicyhek/mybuilds/pull/1))

## 1.0.0 (2026-11-11)

- The first deployed version: signed ingestion from GitHub webhooks, CI events and Argo CD notifications, release
  projection, the API, Server-Sent Events and the UI. The ingestion contract in docs/ingestion.md has only gained
  fields since, so no later change needed a major bump.
