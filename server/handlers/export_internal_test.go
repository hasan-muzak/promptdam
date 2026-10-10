package handlers

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/analytics"
)

func newLedger(rows []analytics.Request, next *analytics.Cursor,
	page func(context.Context, *analytics.Cursor) ([]analytics.Request, *analytics.Cursor, error)) *ledgerCSV {
	l := &ledgerCSV{ctx: context.Background(), next: next, page: page}
	l.out = csv.NewWriter(&l.buf)
	_ = l.out.Write(ledgerHeader)
	l.add(rows)
	return l
}

func TestLedgerReadsEveryPage(t *testing.T) {
	pages := [][]analytics.Request{{{Trace: "req_2"}}, {{Trace: "req_1"}}}
	l := newLedger([]analytics.Request{{Trace: "req_3"}}, &analytics.Cursor{},
		func(context.Context, *analytics.Cursor) ([]analytics.Request, *analytics.Cursor, error) {
			rows := pages[0]
			pages = pages[1:]
			if len(pages) == 0 {
				return rows, nil, nil
			}
			return rows, &analytics.Cursor{}, nil
		})
	lines, err := csv.NewReader(l).ReadAll()
	if err != nil || len(lines) != 4 || lines[0][0] != "time" || lines[1][1] != "req_3" || lines[3][1] != "req_1" {
		t.Fatalf("lines %v, %v", lines, err)
	}
}

// A page that fails part way is a read error, which muzak answers by
// aborting the transfer: the client never holds a file that ends cleanly
// short.
func TestLedgerFailsWhenAPageFails(t *testing.T) {
	broke := errors.New("clickhouse went away")
	l := newLedger([]analytics.Request{{Trace: "req_2"}}, &analytics.Cursor{},
		func(context.Context, *analytics.Cursor) ([]analytics.Request, *analytics.Cursor, error) {
			return nil, nil, broke
		})
	got, err := io.ReadAll(l)
	if !errors.Is(err, broke) || !strings.Contains(string(got), "req_2") {
		t.Fatalf("read %q, %v", got, err)
	}
}
