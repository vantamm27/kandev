package kandevplugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kandev/kandev/internal/plugins/manifest"
	"github.com/kandev/kandev/internal/plugins/pkgtar"
)

// TestManifestIsValid runs Kandev's own manifest parser and validator.
func TestManifestIsValid(t *testing.T) {
	raw, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatalf("manifest rejected by Kandev: %v", err)
	}
	if m.ID != "kandev-fleet" {
		t.Fatalf("id = %q", m.ID)
	}
}

// TestPackageInstalls builds the host archive with plugin-pack and installs
// it with Kandev's installer, which checks archive safety, checksums, and
// the host executable.
func TestPackageInstalls(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the plugin package")
	}
	key := runtime.GOOS + "-" + runtime.GOARCH
	switch key {
	case "linux-amd64", "linux-arm64", "darwin-arm64":
	default:
		t.Skipf("manifest declares no executable for %s", key)
	}
	cmd := exec.Command("make", "package-host")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make package-host: %v\n%s", err, out)
	}
	matches, err := filepath.Glob(filepath.Join(".build", "kandev-fleet-*.tar.gz"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("archive = %v, %v", matches, err)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := pkgtar.Install(f, t.TempDir()); err != nil {
		t.Fatalf("Kandev installer rejected the package: %v", err)
	}
}
