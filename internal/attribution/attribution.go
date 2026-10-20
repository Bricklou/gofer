// Package attribution lists the third-party software built into Gofer, for
// the About page, and carries the full license texts. scripts/licenses.sh
// generates go_modules.csv and notices.txt; bundled.csv is kept by hand.
package attribution

import (
	_ "embed"
	"encoding/csv"
	"runtime"
	"strings"
)

var (
	//go:embed bundled.csv
	bundledCSV string
	//go:embed go_modules.csv
	goModulesCSV string
	//go:embed notices.txt
	notices string
)

// Library is one piece of third-party software.
type Library struct {
	Name    string
	Version string
	License string
	URL     string
}

// Notices returns every third-party license text, as one plain-text file.
func Notices() string { return notices }

// Bundled returns the software that is not a Go module: the Go standard
// library, SQLite, and the scripts, styles, icons and fonts the pages use.
func Bundled() []Library {
	var libs []Library
	for i, row := range readCSV(bundledCSV) {
		if i == 0 || len(row) < 4 {
			continue // the header
		}
		lib := Library{Name: row[0], Version: row[1], License: row[2], URL: row[3]}
		if lib.Name == "Go standard library" && lib.Version == "" {
			lib.Version = runtime.Version()
		}
		libs = append(libs, lib)
	}
	return libs
}

// GoModules returns the Go modules compiled into Gofer, one entry for each
// with all of its licenses.
func GoModules() []Library {
	var libs []Library
	for _, row := range readCSV(goModulesCSV) {
		if len(row) < 3 {
			continue
		}
		if n := len(libs); n > 0 && libs[n-1].Name == row[0] {
			libs[n-1].License += ", " + row[2]
			continue
		}
		libs = append(libs, Library{Name: row[0], Version: row[1], License: row[2], URL: "https://pkg.go.dev/" + row[0]})
	}
	return libs
}

func readCSV(s string) [][]string {
	rows, err := csv.NewReader(strings.NewReader(s)).ReadAll()
	if err != nil {
		panic("attribution: " + err.Error())
	}
	return rows
}
