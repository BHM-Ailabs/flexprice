package helm_test

import (
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

const scriptPath = "files/bootstrap-temporal-search-attributes.sh"

func renderBootstrap(t *testing.T, ch *chart.Chart) (string, error) {
	t.Helper()
	values, err := chartutil.ToRenderValues(ch, map[string]interface{}{
		"secrets": map[string]interface{}{"existingSecret": "test-only"},
	}, chartutil.ReleaseOptions{Name: "test", Namespace: "test", Revision: 1, IsInstall: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(ch, values)
	return rendered[ch.Name()+"/templates/jobs/temporal-namespace-bootstrap.yaml"], err
}

func TestRealChartPackagesAndRendersTemporalBootstrap(t *testing.T) {
	ch, err := loader.Load("../flexprice")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packaged, err := loader.Load(archive)
	if err != nil {
		t.Fatal(err)
	}
	for name, loaded := range map[string]*chart.Chart{"directory": ch, "archive": packaged} {
		t.Run(name, func(t *testing.T) {
			job, err := renderBootstrap(t, loaded)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"kind: Job", "operator search-attribute create", "SubscriptionID TenantID EnvironmentID FailingActivity FailureReason", `export TEMPORAL_NAMESPACE="default"`} {
				if !strings.Contains(job, want) {
					t.Fatalf("bootstrap Job is missing %q", want)
				}
			}
			for _, file := range loaded.Files {
				if file.Name == "provision.sh" {
					t.Fatal("root provisioning script must remain excluded")
				}
			}
		})
	}
}

func TestTemporalBootstrapFailsRenderingWithoutScript(t *testing.T) {
	ch, err := loader.Load("../flexprice")
	if err != nil {
		t.Fatal(err)
	}
	files := ch.Files[:0]
	for _, file := range ch.Files {
		if file.Name != scriptPath {
			files = append(files, file)
		}
	}
	ch.Files = files
	_, err = renderBootstrap(t, ch)
	if err == nil || !strings.Contains(err.Error(), "Temporal bootstrap script missing from chart package") {
		t.Fatalf("missing script must fail rendering, got %v", err)
	}
}
