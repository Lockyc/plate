// Package tsv reads the tab-separated tables plate and its scripts write: a
// header row naming the columns, then one row per record, each as long as
// the header.
package tsv

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Table is a file read under its header.
type Table struct {
	Path string
	Head []string
	Rows [][]string
}

// Read reads path, skipping blank lines. It refuses a file with no header,
// a header lacking any of want, and a row shorter than the header.
func Read(path string, want ...string) (*Table, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := &Table{Path: path}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		r := strings.Split(line, "\t")
		if t.Head == nil {
			t.Head = r
			continue
		}
		if len(r) < len(t.Head) {
			return nil, fmt.Errorf("%s: short row %q", path, line)
		}
		t.Rows = append(t.Rows, r)
	}
	if t.Head == nil {
		return nil, fmt.Errorf("%s is empty: want a header row", path)
	}
	for _, w := range want {
		if t.Col(w) < 0 {
			return nil, fmt.Errorf("%s: want %s columns, header is %q", path, strings.Join(want, ", "), strings.Join(t.Head, "\t"))
		}
	}
	return t, nil
}

// Col is the index of column name, or -1 when the header lacks it.
func (t *Table) Col(name string) int { return slices.Index(t.Head, name) }

// Get is row i's value in column name, which Read was asked to want.
func (t *Table) Get(i int, name string) string { return t.Rows[i][t.Col(name)] }
