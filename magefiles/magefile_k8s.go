//go:build mage

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"iormesh/internal/k8s"
)

// k8sManifestDir holds the generated Kubernetes manifests (docs/milestones.md
// M7, "Suggested repo layout": deploy/).
const k8sManifestDir = "deploy"

// K8sManifests regenerates deploy/ - the prepare patch, the sidecar patch
// and kubectl debug's --custom profile - from internal/k8s (decisions #13,
// #14, #18), with k8s.Defaults(). internal/k8s's golden test fails while
// deploy/ differs from what this writes, so edit the generator, never the
// files.
func K8sManifests() error {
	files, err := k8s.Defaults().Files()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(k8sManifestDir, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(k8sManifestDir, name)
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}
