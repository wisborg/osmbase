// Package importcheck says what a package compiles in, for the structural
// tests that hold a package to what it can reach: the renderer and the store
// can never fetch anything, the bounded decompression can never reach the
// rest of the module.
//
// Those promises are about everything a package compiles in, not only its
// own import list. A renderer that imports nothing from the network still
// fetches if a package it imports does, so a test that read only the
// direct imports would pass while the promise was broken one import away --
// which is where a change made for another reason would break it.
//
// The graph comes from `go list -deps`, the go command's own answer, rather
// than from walking go/build by hand: in module mode go/build asks the go
// command anyway, and a walk of our own would be a second resolver to get
// wrong. Only a package's own files count, not its tests, as with
// build.ImportDir in mode 0: a fixture may use anything.
package importcheck

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// Graph is each package a package compiles in, mapped to its direct
// imports.
type Graph map[string][]string

// Load is the graph of the package in the current directory -- a test's own
// package, since `go test` runs a test in its package's directory -- and the
// package's import path. It fails the test where the go command cannot say:
// a structural test that skipped would assert nothing while reading as a
// pass.
func Load(t testing.TB) (root string, g Graph) {
	t.Helper()
	gotool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("importcheck: the go command is not on PATH, and it is what says what this package compiles in: %v", err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(gotool, "list", "-deps", "-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`, ".")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("importcheck: go list: %v\n%s", err, stderr.String())
	}
	g = Graph{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		g[fields[0]] = fields[1:]
		// -deps lists dependencies before the packages that import
		// them, so the package asked about comes last.
		root = fields[0]
	}
	if root == "" {
		t.Fatal("importcheck: go list listed no packages")
	}
	return root, g
}

// Chain is the shortest chain of imports by which from compiles in to --
// from, then each package imported on the way, ending with to -- or nil
// where it does not. A failing test prints it, so the reader is told which
// import to look at rather than left to search for one.
func (g Graph) Chain(from, to string) []string {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if p == to {
			var chain []string
			for ; p != ""; p = prev[p] {
				chain = append([]string{p}, chain...)
			}
			return chain
		}
		for _, imp := range g[p] {
			if _, seen := prev[imp]; !seen {
				prev[imp] = p
				queue = append(queue, imp)
			}
		}
	}
	return nil
}

// Reaches is every package of g whose import path is banned, or starts with
// one of the banned prefixes that end in a slash, with the chain by which
// root compiles it in.
func (g Graph) Reaches(root string, banned []string) map[string][]string {
	found := map[string][]string{}
	for pkg := range g {
		if pkg == root {
			continue
		}
		for _, b := range banned {
			if pkg == b || strings.HasSuffix(b, "/") && strings.HasPrefix(pkg, b) {
				found[pkg] = g.Chain(root, pkg)
				break
			}
		}
	}
	return found
}
