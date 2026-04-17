//go:build integration

package integration

// TestImageInstall_Capabilities drives the capabilities mechanism
// end-to-end: build THIS provider's image from the repo Dockerfile,
// push it to a local Docker registry so the manifest digest is
// pullable, install via `mgtt provider install --image <digest-ref>`,
// and verify the caps + network declarations surface in the install
// output and on disk. Then `docker run --rm <image> version` to
// confirm the binary entrypoint implements the probe protocol.
//
// Scope: proves the provider image correctly declares its needs/network
// (via the extracted /manifest.yaml), that mgtt reads those declarations
// during install, and that the image is a valid probe-protocol binary.
// It does NOT run real probes — those need a backend and live in other
// integration tests in this repo.
//
// Requirements: docker on PATH, mgtt on PATH. Skipped when either is
// absent. Run with:
//
//	go test -tags=integration ./test/integration/ -run TestImageInstall_Capabilities -v

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Provider-specific fixtures. Update these alongside manifest.yaml.
const (
	imgLocalTag    = "mgtt-provider-terraform-it:test"
	providerName   = "terraform"
	expectCaps     = "terraform, aws"
	expectNetwork  = "host"
	registryPort   = "15803"
	registryName   = "mgtt-provider-terraform-it-registry"
	localPushTag   = "localhost:15803/mgtt-provider-terraform:it"
)

func TestImageInstall_Capabilities(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if _, err := exec.LookPath("mgtt"); err != nil {
		t.Skip("mgtt CLI not on PATH; install mgtt from github.com/mgt-tool/mgtt main for this test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// 1. Build image from the provider's Dockerfile.
	t.Logf("building %s from %s", imgLocalTag, repoRoot)
	build := exec.CommandContext(ctx, "docker", "build", "-t", imgLocalTag, repoRoot)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", imgLocalTag).Run() })

	// 2. Bring up a local registry so we can push and get a pullable digest.
	_ = exec.Command("docker", "rm", "-f", registryName).Run()
	regStart := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", registryName,
		"-p", registryPort+":5000",
		"registry:2",
	)
	if out, err := regStart.CombinedOutput(); err != nil {
		t.Fatalf("start local registry: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", registryName).Run() })

	// Wait for registry.
	if !waitForRegistry(t, registryPort, 30*time.Second) {
		t.Fatal("registry did not become ready")
	}

	// 3. Tag + push.
	if out, err := exec.CommandContext(ctx, "docker", "tag", imgLocalTag, localPushTag).CombinedOutput(); err != nil {
		t.Fatalf("docker tag: %v\n%s", err, out)
	}
	pushOut, err := exec.CommandContext(ctx, "docker", "push", localPushTag).CombinedOutput()
	if err != nil {
		t.Fatalf("docker push: %v\n%s", err, pushOut)
	}

	// 4. Fetch the manifest digest from the registry's v2 API.
	digest := fetchManifestDigest(t, registryPort, "mgtt-provider-terraform", "it")
	if digest == "" {
		t.Fatal("could not resolve manifest digest after push")
	}
	digestRef := fmt.Sprintf("localhost:%s/mgtt-provider-terraform@%s", registryPort, digest)
	t.Logf("digest: %s", digest)

	// 5. Install via `mgtt provider install --image` into an isolated MGTT_HOME.
	home := t.TempDir()
	installCmd := exec.CommandContext(ctx, "mgtt", "provider", "install", "--image", digestRef)
	installCmd.Env = append(os.Environ(), "MGTT_HOME="+home)
	installOut, err := installCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mgtt provider install --image: %v\n%s", err, installOut)
	}
	outStr := string(installOut)
	t.Logf("install output:\n%s", outStr)

	// 6. Verify caps + network surfaced in the install output.
	if expectCaps != "" {
		capLine := "capabilities: " + expectCaps
		if !strings.Contains(outStr, capLine) {
			t.Errorf("install output must contain %q; got:\n%s", capLine, outStr)
		}
	}
	if expectNetwork != "" && expectNetwork != "bridge" {
		netLine := "network: " + expectNetwork
		if !strings.Contains(outStr, netLine) {
			t.Errorf("install output must contain %q; got:\n%s", netLine, outStr)
		}
	}

	// 7. Verify .mgtt-install.json records method=image.
	metaPath := filepath.Join(home, "providers", providerName, ".mgtt-install.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read install metadata: %v", err)
	}
	var meta struct {
		Method  string `json:"method"`
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse install metadata: %v", err)
	}
	if meta.Method != "image" {
		t.Errorf("want method=image in .mgtt-install.json; got %q", meta.Method)
	}
	if meta.Source != digestRef {
		t.Errorf("want source=%q; got %q", digestRef, meta.Source)
	}

	// 8. docker run <image> version — proves entrypoint + probe protocol wiring.
	verCmd := exec.CommandContext(ctx, "docker", "run", "--rm", imgLocalTag, "version")
	verOut, err := verCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run <image> version: %v\n%s", err, verOut)
	}
	if !strings.Contains(strings.ToLower(string(verOut)), "version") &&
		!strings.ContainsAny(string(verOut), "0123456789") {
		t.Errorf("version subcommand must emit a version string; got %q", verOut)
	}

	// 9. Uninstall cleanly.
	uninst := exec.CommandContext(ctx, "mgtt", "provider", "uninstall", providerName)
	uninst.Env = append(os.Environ(), "MGTT_HOME="+home)
	if out, err := uninst.CombinedOutput(); err != nil {
		t.Errorf("uninstall: %v\n%s", err, out)
	}
}

func waitForRegistry(t *testing.T, port string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:" + port + "/v2/")
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// fetchManifestDigest queries the registry's v2 manifest endpoint with the
// OCI-manifest Accept header and returns the Docker-Content-Digest header,
// which is the @sha256 string that `docker pull` uses for digest-pinning.
func fetchManifestDigest(t *testing.T, port, repo, tag string) string {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%s/v2/%s/manifests/%s", port, repo, tag)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept",
		"application/vnd.docker.distribution.manifest.v2+json, "+
			"application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	defer resp.Body.Close()
	return resp.Header.Get("Docker-Content-Digest")
}
