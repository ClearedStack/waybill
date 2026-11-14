package deploy_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const repoRoot = ".."

func TestHardenedKubernetesManifests(t *testing.T) {
	wbBytes, err := os.ReadFile("waybill.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wb := string(wbBytes)

	requiredDeploymentSnippets := []string{
		"kind: ServiceAccount",
		"kind: Deployment",
		"namespace: cs-workers",
		"serviceAccountName: waybill",
		"key: kubernetes.io/arch",
		"operator: In",
		"- arm64",
		"key: kubernetes.io/hostname",
		"operator: NotIn",
		"- cm3588-plus",
		"runAsNonRoot: true",
		"runAsUser: 65532",
		"seccompProfile:",
		"type: RuntimeDefault",
		"privileged: false",
		"allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true",
		"drop:",
		"- ALL",
		"memory: 20Mi",
		"memory: 30Mi",
		"mountPath: /var/spool/cs",
		"CONTRACT_VERSION",
		"MUSTER_URL",
	}
	for _, want := range requiredDeploymentSnippets {
		if !strings.Contains(wb, want) {
			t.Errorf("deploy/waybill.yaml missing required snippet %q", want)
		}
	}

	digestRE := regexp.MustCompile(`image:\s+\S+@sha256:[0-9a-f]{64}`)
	if !digestRE.MatchString(wb) {
		t.Errorf("deploy/waybill.yaml image is not pinned by sha256 digest")
	}

	npBytes, err := os.ReadFile("networkpolicy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	np := string(npBytes)

	requiredPolicySnippets := []string{
		"kind: NetworkPolicy",
		"name: default-deny",
		"namespace: cs-workers",
		"podSelector: {}",
		"- Ingress",
		"- Egress",
		"name: waybill-egress",
		"ingress: []",
		"kubernetes.io/metadata.name: clearedstack",
		"app.kubernetes.io/name: muster",
		"kubernetes.io/metadata.name: kube-system",
		"port: 53",
		"port: 8080",
	}
	for _, want := range requiredPolicySnippets {
		if !strings.Contains(np, want) {
			t.Errorf("deploy/networkpolicy.yaml missing required snippet %q", want)
		}
	}
}

func TestKoConfigAndNoDockerfile(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot, "Dockerfile")); !os.IsNotExist(err) {
		t.Fatalf("Dockerfile must not exist; Waybill builds with ko")
	}
	koBytes, err := os.ReadFile(filepath.Join(repoRoot, ".ko.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ko := string(koBytes)
	digestRE := regexp.MustCompile(`defaultBaseImage:\s+\S+@sha256:[0-9a-f]{64}`)
	if !digestRE.MatchString(ko) {
		t.Errorf(".ko.yaml defaultBaseImage must be pinned by sha256 digest")
	}
	for _, want := range []string{"linux/arm64", "CGO_ENABLED=0", "./cmd/waybill"} {
		if !strings.Contains(ko, want) {
			t.Errorf(".ko.yaml missing %q", want)
		}
	}
}

func TestGoModStandardLibraryOnly(t *testing.T) {
	modBytes, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mod := string(modBytes)
	if !strings.Contains(mod, "module github.com/ClearedStack/waybill\n") {
		t.Errorf("unexpected module path in go.mod:\n%s", mod)
	}
	if strings.Contains(mod, "require") {
		t.Errorf("go.mod must have zero external dependencies:\n%s", mod)
	}
}

func TestNoEmDashesAnywhereInRepo(t *testing.T) {
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "\u2014") {
			t.Errorf("file %s contains a forbidden em dash (U+2014)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
