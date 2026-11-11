package mysqldb_test

import (
	"testing"

	"vending/internal/repository/mysqldb"
)

// Rows builds the VALUES list of a bulk insert, and the thing worth checking is
// that the shape matches what the caller will pass as arguments: a drifted
// column count is a runtime error thousands of rows into a demo load.
func TestRows(t *testing.T) {
	cases := []struct {
		n, c int
		want string
	}{
		{1, 1, "(?)"},
		{1, 3, "(?,?,?)"},
		{3, 2, "(?,?),(?,?),(?,?)"},
		{0, 5, ""}, // nothing to write: not a valid INSERT, and the caller must skip it
		{2, 0, ""}, // no columns is not an insert either
		{-1, 3, ""},
	}
	for _, c := range cases {
		if got := mysqldb.Rows(c.n, c.c); got != c.want {
			t.Errorf("Rows(%d, %d) = %q, want %q", c.n, c.c, got, c.want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, ""}, {1, "?"}, {4, "?,?,?,?"}} {
		if got := mysqldb.Placeholders(c.n); got != c.want {
			t.Errorf("Placeholders(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
