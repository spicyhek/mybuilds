package services

import "testing"

func TestBuiltInMap(t *testing.T) {
	m, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := m.ByWorkflow("SpicyHek/Homelab-Hosted-Personal-Site", ".github/workflows/release.yml@refs/heads/main")
	if !ok || svc.Name != "personal-site" || svc.ArgoCDApplication != "personal-site" {
		t.Fatalf("ByWorkflow = %+v, %v", svc, ok)
	}
	if _, ok := m.ByWorkflow("spicyhek/homelab-hosted-personal-site", ".github/workflows/ci.yml"); ok {
		t.Error("another workflow of the repository matched")
	}
}

func TestStageForStep(t *testing.T) {
	m, _ := Load("")
	svc, _ := m.ByName("personal-site")
	steps := []string{"Set up job", "Smoke test", "Push image", "Resolve digest", "Complete job"}
	want := []string{"build", "build", "publish", "publish", "publish"}
	for i, w := range want {
		if got, ok := svc.StageForStep("build", steps, i); !ok || got != w {
			t.Errorf("build step %q: stage %q, want %q", steps[i], got, w)
		}
	}
	// A renamed step lands in its job's default stage instead of vanishing.
	if got, _ := svc.StageForStep("build", []string{"Compile"}, 0); got != "build" {
		t.Errorf("unmatched step: stage %q", got)
	}
	if got, _ := svc.StageForStep("bump-gitops", []string{"Squash merge"}, 0); got != "gitops" {
		t.Errorf("bump-gitops: stage %q", got)
	}
	if _, ok := svc.StageForStep("test", []string{"go test"}, 0); ok {
		t.Error("a job missing from the map was given a stage")
	}
}

func TestParseRejectsBadMaps(t *testing.T) {
	for name, doc := range map[string]string{
		"bad name":  `{"services":[{"name":"Bad Name","environment":"production","repository":"a/b","workflow":"w","branch":"main"}]}`,
		"duplicate": `{"services":[{"name":"a","environment":"p","repository":"a/b","workflow":"w","branch":"main"},{"name":"a","environment":"p","repository":"a/b","workflow":"w","branch":"main"}]}`,
		"missing":   `{"services":[{"name":"a"}]}`,
		"not json":  `services:`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
