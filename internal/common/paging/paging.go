// Package paging turns the page number and page size an admin screen asks for
// into the limit and offset a query needs.
//
// It exists because two screens ask the same way — the transaction history and
// the restock history — and a page size clamped in one of them and not the
// other is the kind of difference nobody notices until an export times out.
//
// Everything here is a decision about what a person may ask for, which is why
// it is in common and not in a driver: a repository that chose its own ceiling
// would be deciding policy from the bottom of the stack.
package paging

const (
	// MaxSize is the largest page a screen may ask for. Past a couple of
	// hundred rows a page is not read, it is scrolled past.
	MaxSize = 200

	// MaxExportRows caps "give me everything", which is what the CSV export
	// asks for. It is a safety limit, not a page: an export that would exceed
	// it is a sign the span wants narrowing.
	MaxExportRows = 100_000
)

// Page is a validated page request: never a number below 1, never a size above
// MaxSize.
type Page struct {
	Number int
	Size   int
}

// Of clamps what the query string asked for. A missing or nonsensical size
// becomes def, which each screen chooses for itself because a transaction row
// and a restock visit are not the same height.
func Of(number, size, def int) Page {
	if number < 1 {
		number = 1
	}
	if size < 1 {
		size = def
	}
	if size > MaxSize {
		size = MaxSize
	}
	return Page{Number: number, Size: size}
}

// Window is the LIMIT and OFFSET for this page. When all is true it is the
// whole filtered set instead, from the first row, capped at MaxExportRows: the
// export wants every matching row, and page numbers mean nothing to it.
func (p Page) Window(all bool) (limit, offset int) {
	if all {
		return MaxExportRows, 0
	}
	return p.Size, (p.Number - 1) * p.Size
}
