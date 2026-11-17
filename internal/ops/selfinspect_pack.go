package ops

import (
	"context"
	"fmt"
	"strings"
)

// The nightly self-inspection's results as an inspection pack item
// ("self_inspection", inspack_sections.go): every night's score, every
// test's result each night, the exceptions open at any time in the period
// with how each was handled, and the mutes in force. Examples are the masked
// ones the self-inspection keeps; nothing else about clients is added.
func init() {
	packSelfInspection = func(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
		from, to := b.Span(it)
		if from == "" {
			from = to
		}
		r, err := b.O.SelfInspectionSummary(ctx, b.Tenant, from, to)
		if err != nil {
			return PackPart{}, err
		}
		if len(r.Runs) == 0 {
			return PackPart{NotAvailable: fmt.Sprintf("The self-inspection did not run between %s and %s.", from, to)}, nil
		}
		runs := [][]string{{"Business date", "Run at", "Trigger", "By", "Score", "Passed", "Failed", "Skipped", "Muted"}}
		results := [][]string{{"Business date", "Test", "Title", "Area", "Severity", "Owner", "Source", "Status", "Checked", "Found", "Summary", "Examples (masked)"}}
		for _, run := range r.Runs {
			runs = append(runs, []string{run.Date, run.At, run.Trigger, run.By, fmt.Sprintf("%.1f", run.Score),
				fmt.Sprint(run.Passed), fmt.Sprint(run.Failed), fmt.Sprint(run.Skipped), fmt.Sprint(run.Muted)})
			for _, x := range run.Results {
				results = append(results, []string{run.Date, x.Test, x.Title, x.Area, x.Severity, x.Owner, x.Source, x.Status,
					fmt.Sprint(x.Checked), fmt.Sprint(x.Found), x.Summary, strings.Join(x.Examples, "; ")})
			}
		}
		exc := [][]string{{"Test", "Title", "Severity", "Owner", "Source", "First found", "Last found", "Due", "Status", "Found", "Summary", "How it was handled"}}
		for _, e := range r.Exceptions {
			exc = append(exc, []string{e.Test, e.Title, e.Severity, e.Owner, e.Source, e.Opened, e.LastDate, e.Due,
				firstOf(e.StatusName, e.Status), fmt.Sprint(e.Found), e.Summary, e.Handled})
		}
		mutes := [][]string{{"Test", "Reason", "From", "Until", "By", "At", "Ended at", "Ended by", "Why it ended"}}
		for _, m := range r.Mutes {
			mutes = append(mutes, []string{m.Test, m.Reason, m.From, m.Until, m.By, m.At, m.EndedAt, m.EndedBy, m.EndReason})
		}
		files := []PackFile{
			b.CSV("self_inspection_runs.csv", runs),
			b.CSV("self_inspection_results.csv", results),
			b.CSV("self_inspection_exceptions.csv", exc),
		}
		if len(mutes) > 1 {
			files = append(files, b.CSV("self_inspection_mutes.csv", mutes))
		}
		plural := func(n int, one, many string) string {
			if n == 1 {
				return "1 " + one
			}
			return fmt.Sprintf("%d %s", n, many)
		}
		note := fmt.Sprintf("%s; average score %.1f (first %.1f, last %.1f); %s, %d still open at the period end",
			plural(r.Totals.Runs, "run", "runs"), r.Totals.AverageScore, r.Totals.FirstScore, r.Totals.LastScore,
			plural(r.Totals.Exceptions, "exception", "exceptions"), r.Totals.Open)
		if r.Totals.Waiting > 0 {
			note += fmt.Sprintf("; %d tests are waiting for data Ledgerline does not have yet", r.Totals.Waiting)
		}
		return PackPart{Files: files, Records: len(results) - 1, Note: note}, nil
	}
}
