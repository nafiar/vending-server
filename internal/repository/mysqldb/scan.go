package mysqldb

import (
	"database/sql"
	"strings"
	"time"
)

// RowScanner is one row, so a scan helper works for both QueryRow and Rows.
type RowScanner interface{ Scan(dest ...any) error }

// Ms is a time as the Unix milliseconds the API reports.
func Ms(t time.Time) int64 { return t.UnixMilli() }

// NullMs is a nullable time as a pointer to Unix milliseconds, so JSON gets null.
func NullMs(t sql.NullTime) *int64 {
	if !t.Valid {
		return nil
	}
	v := t.Time.UnixMilli()
	return &v
}

// NullString turns a nullable column into a pointer, so JSON gets null.
func NullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// NullInt turns a nullable column into a pointer, so JSON gets null.
func NullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// StrOrNil writes an empty string to the database as NULL.
func StrOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Placeholders is "?,?,?" for an IN clause of n values.
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// Rows is the VALUES list of a multi-row INSERT: n rows of c columns each,
// as "(?, ?),(?, ?),(?, ?)". It is here beside Placeholders because a bulk
// insert built by hand in a driver is where a column count drifts away from
// the argument list.
//
// It returns "" for no rows, which is not a valid INSERT: a caller with nothing
// to write must not run the statement at all.
func Rows(n, c int) string {
	if n <= 0 || c <= 0 {
		return ""
	}
	one := "(" + Placeholders(c) + ")"
	return strings.TrimSuffix(strings.Repeat(one+",", n), ",")
}

// Args turns ids into the args a query takes.
func Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}
