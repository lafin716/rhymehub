// Package sdkdist embeds the packed SDK tarballs (@rhymehub/shared, @rhymehub/sdk, @rhymehub/react) so the core
// can serve them to managed apps. The packages are not published to a registry; `make web` packs them into
// tarballs/. A plain `go build` without that step embeds no tarballs and Lookup reports every file as missing.
package sdkdist

import (
	"embed"
	"io/fs"
	"regexp"
)

//go:embed all:tarballs
var files embed.FS

// FallbackVersion is the SDK version assumed when no tarball is embedded.
const FallbackVersion = "0.1.0"

// Package is one SDK package in install order (dependencies first).
type Package struct {
	Name string `json:"name"` // npm package name
	File string `json:"file"` // tarball file name
}

// fileRE is the only shape of file name that can be served; it also rules out path traversal.
var fileRE = regexp.MustCompile(`^rhymehub-(shared|sdk|react)-([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]+)?)\.tgz$`)

var names = []struct{ short, pkg string }{
	{"shared", "@rhymehub/shared"},
	{"sdk", "@rhymehub/sdk"},
	{"react", "@rhymehub/react"},
}

// Version returns the version of the embedded tarballs, or FallbackVersion when none is embedded.
func Version() string {
	entries, err := fs.ReadDir(files, "tarballs")
	if err != nil {
		return FallbackVersion
	}
	for _, e := range entries {
		if m := fileRE.FindStringSubmatch(e.Name()); m != nil && m[1] == "react" {
			return m[2]
		}
	}
	return FallbackVersion
}

// Packages lists the three SDK packages for Version().
func Packages() []Package {
	v := Version()
	out := make([]Package, 0, len(names))
	for _, n := range names {
		out = append(out, Package{Name: n.pkg, File: "rhymehub-" + n.short + "-" + v + ".tgz"})
	}
	return out
}

// Available reports whether every tarball listed by Packages is embedded.
func Available() bool {
	for _, p := range Packages() {
		if _, ok := Lookup(p.File); !ok {
			return false
		}
	}
	return true
}

// Lookup returns the tarball bytes for a file name of the form rhymehub-<pkg>-<version>.tgz.
func Lookup(name string) ([]byte, bool) {
	if !fileRE.MatchString(name) {
		return nil, false
	}
	b, err := files.ReadFile("tarballs/" + name)
	if err != nil {
		return nil, false
	}
	return b, true
}
