package voiceassets

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Executable resolves an override or an installed binary without executing it.
// Explicit paths never fall back to another executable.
func Executable(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return filepath.Abs(path)
	}
	if name != "" && !strings.ContainsRune(name, filepath.Separator) {
		dirs := []string{}
		if exe, err := os.Executable(); err == nil {
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				dirs = append(dirs, filepath.Dir(real))
			}
			dirs = append(dirs, filepath.Dir(exe))
		}
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, ".local", "bin"))
		}
		dirs = append(dirs, "/usr/local/bin", "/usr/bin")
		for _, dir := range dirs {
			if path, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
				return filepath.Abs(path)
			}
		}
	}
	return "", fmt.Errorf("installed voice executable %q not found; install it or set its executable override (see voice setup --help)", name)
}

// Roots lists model search locations, resolving the helper's installation prefix.
// The first is the setup destination; bundle and XDG system assets are fallbacks.
func Roots(dataDir, helper string) []string {
	roots := []string{filepath.Join(dataDir, "voice", Profile), filepath.Join(dataDir, "models")}
	if helper != "" {
		if real, err := filepath.EvalSymlinks(helper); err == nil {
			helper = real
		}
		prefix := filepath.Dir(filepath.Dir(helper))
		roots = append(roots, filepath.Join(prefix, "share", "ginkgo", "voice", Profile), filepath.Join(prefix, "models"))
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, dir := range filepath.SplitList(dataDirs) {
		if filepath.IsAbs(dir) {
			roots = append(roots, filepath.Join(dir, "ginkgo", "voice", Profile))
		}
	}
	return roots
}

// Location is a discovered standard asset or an explicit user override.
type Location struct {
	Asset
	Filename string
	Override bool
}

// Resolve fills only empty settings. Missing defaults point at the setup
// destination, so setup and runtime always agree on the same paths.
func Resolve(roots []string, overrides map[string]string) []Location {
	var locations []Location
	for _, asset := range Assets() {
		name := overrides[asset.Key]
		custom := name != ""
		if asset.Key == "" {
			// The pinned encoder references this sidecar by relative filename.
			// Custom encoders may be self-contained or use other sidecars; their
			// actual dependencies are checked by native initialization.
			if overrides["voice.sherpa.parakeet.encoder"] != "" {
				continue
			}
			name = filepath.Join(filepath.Dir(locations[0].Filename), "encoder.weights")
		}
		if name == "" {
			name = filepath.Join(roots[0], filepath.FromSlash(asset.Path))
			for _, root := range roots {
				candidate := filepath.Join(root, filepath.FromSlash(asset.Path))
				if _, err := os.Lstat(candidate); err == nil || !os.IsNotExist(err) {
					name = candidate
					break
				}
			}
		}
		locations = append(locations, Location{Asset: asset, Filename: name, Override: custom})
	}
	return locations
}
