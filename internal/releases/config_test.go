package releases

import (
	"strings"
	"testing"

	"github.com/spicyhek/mybuilds/internal/events"
)

const (
	configSHA     = "c0ffee0123456789abcdef0123456789abcdef01"
	configRelease = "personal-site/config-c0ffee0/production"
	// servedRelease is the application release the config release's pods
	// carry in their release-id annotation.
	servedRelease = "personal-site/0123456/production/56"
)

func TestParseConfigReleaseID(t *testing.T) {
	if got := ConfigReleaseID("personal-site", "production", configSHA); got != configRelease {
		t.Fatalf("ConfigReleaseID = %q", got)
	}
	p, err := ParseReleaseID(configRelease)
	if err != nil || p.Kind != KindConfig || p.Service != "personal-site" || p.Environment != "production" || p.ShortSHA != "c0ffee0" {
		t.Errorf("got %+v, %v", p, err)
	}
	if p, _ := ParseReleaseID(testRelease); p.Kind != KindApplication {
		t.Errorf("application release parsed as %s", p.Kind)
	}
	if _, err := ParseReleaseID("personal-site/config-c0ffee/production"); err == nil {
		t.Error("a six-character config SHA parsed")
	}
}

type configBuilder struct{ *builder }

func (b configBuilder) sync(trigger string, started, finished int) {
	n := events.ArgoCDNotification{Trigger: trigger, Application: "personal-site", Revision: configSHA,
		StartedAt: tp(started), CommitSubject: "Raise personal-site memory limit", CommitAuthor: "spicyhek"}
	occurred := at(started)
	if finished > 0 {
		n.FinishedAt = tp(finished)
		occurred = at(finished)
	}
	b.add(events.TypeArgoCD, occurred, 1, events.Keys{ReleaseID: configRelease, Revision: configSHA}, n)
}

// rs records the config rollout's new ReplicaSet, whose template carries
// the served release and the digest.
func (b configBuilder) rollout() {
	rb := runtimeBuilder{b.builder}
	rb.dep(101, events.Deployment{Revision: "13", Generation: 59, ObservedGeneration: 59, Replicas: 2,
		StatusReplicas: 3, UpdatedReplicas: 1, ReadyReplicas: 2, AvailableReplicas: 2})
	b.add(events.TypeReplicaSet, at(101), 1, events.Keys{Workload: testWorkload}, events.ReplicaSet{
		Namespace: "default", Name: "personal-site-" + newHash, UID: "rs", Deployment: "personal-site",
		Revision: "13", PodTemplateHash: newHash, CreatedAt: at(101), Replicas: 2,
		Images: []string{testImage + "@" + testDigest}, ReleaseID: servedRelease})
	rb.pod(108, "personal-site-"+newHash+"-a", newHash, 101, true, testDigest, 0, "")
	rb.pod(113, "personal-site-"+newHash+"-b", newHash, 108, true, testDigest, 0, "")
	rb.dep(115, events.Deployment{Revision: "13", Generation: 59, ObservedGeneration: 59, Replicas: 2,
		StatusReplicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2,
		Conditions: progressing("NewReplicaSetAvailable", 114)})
}

func projectConfig(t *testing.T, b *builder, end bool) Projection {
	t.Helper()
	own, rt := split(b)
	runtime := &Runtime{Start: at(100), Events: rt}
	if end {
		next := at(900)
		runtime.End = &next
	}
	p, err := ProjectWithRuntime(configRelease, own, personalSite(t), runtime)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigReleaseRollsOutAndVerifiesTheServedRelease(t *testing.T) {
	b := configBuilder{&builder{t: t}}
	b.sync(events.TriggerSyncRunning, 100, 0)
	b.sync(events.TriggerSyncSucceeded, 100, 103)
	b.rollout()

	p := projectConfig(t, b.builder, false)
	r := p.Release
	if r.Kind != KindConfig || r.Template != TemplateConfig || len(r.Stages) != 4 {
		t.Fatalf("kind %s template %s, %d stages", r.Kind, r.Template, len(r.Stages))
	}
	expect(t, r, R, S, S, S, P)
	if r.GitOpsSHA != configSHA || r.GitOpsSubject != "Raise personal-site memory limit" || r.GitOpsAuthor != "spicyhek" {
		t.Errorf("commit %s %q by %q", r.GitOpsSHA, r.GitOpsSubject, r.GitOpsAuthor)
	}
	if r.RuntimeReleaseID != servedRelease || r.ArtifactDigest != testDigest {
		t.Errorf("runtime release %q digest %q", r.RuntimeReleaseID, r.ArtifactDigest)
	}
	if got := r.Stages[2].Summary; !strings.HasSuffix(got, "serving "+servedRelease) {
		t.Errorf("rollout summary %q", got)
	}

	// Checks expect the served release's X-Release-ID.
	for i := range 5 {
		b.add(events.TypeVerifyCheck, at(120+30*i), 1, events.Keys{ReleaseID: configRelease}, events.VerifyCheck{
			ReleaseID: configRelease, ExpectedReleaseID: servedRelease, CheckedAt: at(120 + 30*i),
			Result: events.CheckPass, DNSOK: true, TLSOK: true, StatusCode: 200, LatencyMS: 90,
			ObservedReleaseID: servedRelease})
	}
	expect(t, projectConfig(t, b.builder, false).Release, S, S, S, S, S)
}

func TestConfigReleaseThatReplacedNoPods(t *testing.T) {
	b := configBuilder{&builder{t: t}}
	b.sync(events.TriggerSyncSucceeded, 100, 103)
	expect(t, projectConfig(t, b.builder, false).Release, R, S, S, P, P)

	b.add(events.TypeNoRollout, at(163), 1, events.Keys{ReleaseID: configRelease},
		events.NoRollout{ReleaseID: configRelease, GraceSeconds: 60, OccurredAt: at(163)})
	p := projectConfig(t, b.builder, false)
	expect(t, p.Release, S, S, S, K, K)
	if got := p.Release.Stages[2].Summary; got != "The sync replaced no pods" {
		t.Errorf("rollout summary %q", got)
	}
}

func TestConfigReleaseFailedSync(t *testing.T) {
	b := configBuilder{&builder{t: t}}
	b.sync(events.TriggerSyncFailed, 100, 103)
	b.add(events.TypeNoRollout, at(163), 1, events.Keys{ReleaseID: configRelease},
		events.NoRollout{ReleaseID: configRelease, GraceSeconds: 60, OccurredAt: at(163)})
	expect(t, projectConfig(t, b.builder, false).Release, F, S, F, K, K)
}

func TestConfigReleaseWithoutAnnotationSkipsVerify(t *testing.T) {
	b := configBuilder{&builder{t: t}}
	b.sync(events.TriggerSyncSucceeded, 100, 103)
	b.rollout()
	for i := range b.evs {
		if b.evs[i].Type == events.TypeReplicaSet {
			var rs events.ReplicaSet
			if err := b.evs[i].Decode(&rs); err != nil {
				t.Fatal(err)
			}
			rs.ReleaseID = ""
			b.evs[i].Payload = []byte(mustJSON(t, rs))
		}
	}
	p := projectConfig(t, b.builder, false)
	expect(t, p.Release, S, S, S, S, K)
	if !strings.Contains(p.Release.Stages[3].Summary, "no release-id annotation") {
		t.Errorf("verify summary %q", p.Release.Stages[3].Summary)
	}
}
