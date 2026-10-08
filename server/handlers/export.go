package handlers

import (
	"encoding/csv"
	"log/slog"
	"strconv"
	"strings"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/ids"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
)

// exportPage is how many calls an export reads at once.
const exportPage = 1000

// ExportSavingsIn asks for a range of the savings ledger.
type ExportSavingsIn struct {
	platform.ProjectPath
	TimeRange
}

// ledgerHeader names the export's columns.
var ledgerHeader = []string{"time", "request", "key", "developer", "user", "route", "family", "requested_model",
	"served_model", "provider", "status", "error_code", "input_tokens", "output_tokens", "cache_read_tokens",
	"cache_write_tokens", "cost_usd", "counterfactual_usd", "saved_usd", "reason", "cache"}

// ExportSavings streams the savings ledger of a range as CSV, a line per
// call, oldest pages last (the request log's order: newest first).
func (h *Handlers) ExportSavings(ctx *muzak.Context, in ExportSavingsIn) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	if err := h.requires(ctx, catalog.LedgerExport); err != nil {
		return muzak.Empty{}, err
	}
	from, to, err := in.bounds(30 * 24 * time.Hour)
	if err != nil {
		return muzak.Empty{}, err
	}
	f := analytics.Filter{From: from, To: to}
	// The first page is read before anything is written, so a bad range or
	// a failing store still answers with an error.
	rows, next, err := h.Analytics.Requests(ctx.Context(), pa.Project.ID, f, nil, exportPage)
	if err != nil {
		return muzak.Empty{}, analyticsFail(err)
	}
	w := ctx.ResponseWriter()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="promptdam-ledger-`+ids.Encode(ids.Project, pa.Project.ID)+"-"+
		from.Format("2006-01-02")+"-"+to.Format("2006-01-02")+`.csv"`)
	out := csv.NewWriter(w)
	_ = out.Write(ledgerHeader)
	for {
		for _, r := range rows {
			_ = out.Write(ledgerLine(r))
		}
		out.Flush()
		if next == nil || out.Error() != nil {
			break
		}
		if rows, next, err = h.Analytics.Requests(ctx.Context(), pa.Project.ID, f, next, exportPage); err != nil {
			// Too late for an error response: the file ends short.
			slog.Warn("handlers: the ledger export broke off", "project", pa.Project.ID, "err", err)
			break
		}
	}
	return muzak.Empty{}, nil
}

func ledgerLine(r analytics.Request) []string {
	usd := func(micros int64) string { return strconv.FormatFloat(float64(micros)/1e6, 'f', 6, 64) }
	n := func(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
	return []string{
		r.TS.UTC().Format(time.RFC3339Nano), r.Trace, ids.Encode(ids.APIKey, r.KeyID), cell(r.Developer), cell(r.User),
		cell(r.Route), r.Family, cell(r.RequestedModel), cell(r.ServedModel), cell(r.Provider), strconv.Itoa(int(r.Status)),
		r.ErrorCode, n(r.InputTokens), n(r.OutputTokens), n(r.CacheReadTokens), n(r.CacheWriteTokens), usd(r.CostMicros),
		usd(r.CostMicros + r.Savings), usd(r.Savings), r.SavingsReason, r.Cache,
	}
}

// cell keeps a client's text from reading as a formula in a spreadsheet.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
