// The following directive is necessary to make the package coherent:
// +build ignore
// It can be invoked by running:
// go generate

package main

import (
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	goPath          = os.Getenv("GOPATH")
	packageTemplate = template.Must(template.New("").Parse(`// Code generated by go generate; DO NOT EDIT.
// This file was generated by robots at
// {{ .Timestamp }}
package statik

const LensGitVersion = "{{ .LensGitVersion }}"
`))
)

// Store the lens git commit version into statik/version.go
func main() {
	// assume lens is cloned and built at $GOPATH/src/github.com/perlin-network/lens
	lensPath := filepath.Join(goPath, "src", "github.com", "perlin-network", "lens")
	lensGitVersionBytes, err := exec.Command("git", "-C", lensPath, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		panic(err)
	}
	lensGitVersion := strings.TrimSpace(string(lensGitVersionBytes))

	// assume already ran "statik -f -src=../../../lens/build -p statik -dest ."
	f, err := os.Create("statik/version.go")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	packageTemplate.Execute(f, struct {
		Timestamp      time.Time
		LensGitVersion string
	}{
		Timestamp:      time.Now(),
		LensGitVersion: lensGitVersion,
	})
}
