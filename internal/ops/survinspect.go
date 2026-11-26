package ops

import (
	"fmt"
	"sort"

	"ledgerline/internal/docs"
)

// ---- self-inspection tests of the surveillance case work ----
//
// They check what survcase.go and survimport.go record: adverse
// observations reported on time, CDSL's alert lists downloaded regularly,
// the checklists worked on closed alerts, and the compliance officer's
// sample of closures (CDSL/DP/SURV/2026/521; CDSL Master Circular for DPs
// 14.5.8-14.5.12; MCX/S&I/251/2026 para 16).

const survSource = "CDSL/DP/SURV/2026/521 (DP2026-521) of 31 Jul 2026, comprehensive surveillance framework for DPs"

func init() {
	siTests = append(siTests,
		SITest{ID: "dp_adverse_7", Title: "Adverse observations reported within 7 days, and the DP's own observations identified within two months", Area: "Depository", Severity: "high", Owner: RoleCompliance,
			Source:    survSource + ", Part I I.2-I.7 (own observations identified within two months of the transaction; adverse observations reported on CDSL's surveillance system within 7 days)",
			ToConfirm: "Whether the 7 days are calendar or working days: Ledgerline counts calendar days, the stricter reading.",
			Checks:    "Adverse observations not reported (or reported late) more than 7 days after they were recorded; the DP's own alerts still undecided more than two months after the transactions they are about.",
			Fix:       "Report the observation on CDSL's surveillance system (or to the exchange) and record the date and reference on the alert; decide the open own alerts, recording why any is late.",
			Screen:    "surv", ScreenName: "Compliance → Surveillance & AML", Real: true, run: siAdverse},
		SITest{ID: "cdsl_alert_downloads", Title: "CDSL surveillance and FIU alerts downloaded regularly", Area: "Depository", Severity: "high", Owner: RoleCompliance,
			Source:    "CDSL/OPS/DP/POLCY/2026/257 (CDSL Master Circular for DPs) 14.5.8-14.5.12: the Designated Director and Principal Officer ensure CDSL's FIU alerts are downloaded periodically and analysed; " + survSource,
			ToConfirm: "CDSL says periodically: the interval is the firm's setting (7 days unless changed).",
			Checks:    "The last download of CDSL's surveillance alerts and of its FIU alerts (or the record that there were none) against the firm's interval. Skipped when the firm is not a CDSL participant.",
			Fix:       "Download both lists from CDAS and load them on Surveillance & AML (or record that there were none).",
			Screen:    "surv", ScreenName: "Compliance → Surveillance & AML", Real: true, run: siCDSLDownloads},
		SITest{ID: "alert_checklist", Title: "CDSL and MCX alerts closed only after their checklist was worked", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
			Source: survSource + ", Part I D and E (nine-point checklist before a response); MCX/S&I/251/2026 para 16",
			Checks: "Closed CDSL, DP and MCX alerts with a checklist point left blank, or marked as having documents attached with none attached.",
			Fix:    "Open the alert, complete the checklist and attach the documents; if the closure was wrong, reopen the matter with a new note.",
			Screen: "surv", ScreenName: "Compliance → Surveillance & AML", Real: true, run: siAlertChecklist},
		SITest{ID: "alert_sample", Title: "A sample of each quarter's alert closures checked by the compliance officer", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
			Source:    survSource + ": closures checked by the compliance officer on a sample basis",
			ToConfirm: "The sample size and when it is checked are not fixed by the text Ledgerline has: the firm's setting (10%, at least 5) by the quarterly status's due date.",
			Checks:    "For the quarter ended before the date, once the quarterly status is due (15 days after the quarter), sampled closures not yet checked.",
			Fix:       "On Surveillance & AML → Sample of closures, check each sampled alert and record Agree or Disagree.",
			Screen:    "surv", ScreenName: "Compliance → Surveillance & AML", Real: true, run: siAlertSample},
	)
}

func siAdverse(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Alerts", e.date)
	as, err := e.o.Alerts(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	for _, a := range as {
		if x := a.Adverse; x != nil {
			s.checked++
			switch {
			case x.ReportedOn == "" && e.date > x.Due:
				s.bad(fmt.Sprintf("%s · %s · %s · adverse since %s, not reported to %s (due %s)", a.ID, a.Type, siClient(a.UCC), x.DecidedOn, x.To, x.Due))
			case x.ReportedOn != "" && x.ReportedOn > x.Due:
				s.bad(fmt.Sprintf("%s · %s · %s · reported on %s, due %s", a.ID, a.Type, siClient(a.UCC), x.ReportedOn, x.Due))
			}
			continue
		}
		dl := ownObsDeadline(a)
		if dl == "" || a.Status == "Closed" || a.Status == "STR filed" || a.Date > e.date {
			continue
		}
		s.checked++
		if e.date > dl {
			s.bad(fmt.Sprintf("%s · %s · %s · transactions to %s, still undecided after %s", a.ID, a.Type, siClient(a.UCC), firstOf(a.TxnTo, a.TxnFrom), dl))
		}
	}
	s.summary = fmt.Sprintf("%d adverse observations and open own DP alerts; %d late.", s.checked, s.found)
	return s, nil
}

func siCDSLDownloads(e *siEnv) (siOut, error) {
	if e.o.firmFacts(e.ctx, e.tenant).CDSL == triNo {
		return siSkip("The firm is not a CDSL participant (Settings → Firm profile).")
	}
	var s siOut
	s.input("Alert download log", e.date)
	xs, err := docs.List[SurvDownload](e.ctx, e.o.Docs, e.tenant, cSurvDownload, "")
	if err != nil {
		return s, err
	}
	days := e.o.DPSurvConfig(e.ctx, e.tenant).DownloadDays
	for _, k := range []string{"cdsl-surv", "cdsl-fiu"} {
		s.checked++
		last := lastDownload(xs, k)
		switch {
		case last == "":
			s.bad(fmt.Sprintf("%s · never downloaded", survImportKinds[k].Title))
		case daysBetween(last, e.date) > days:
			s.bad(fmt.Sprintf("%s · last downloaded %s, %d days ago (every %d days)", survImportKinds[k].Title, last, daysBetween(last, e.date), days))
		}
	}
	s.summary = fmt.Sprintf("%d of CDSL's alert lists checked; %d not downloaded within %d days.", s.checked, s.found, days)
	return s, nil
}

func siAlertChecklist(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Alerts", e.date)
	as, err := e.o.Alerts(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	for _, a := range as {
		if a.Status != "Closed" || checklistFor(a) == nil {
			continue
		}
		s.checked++
		if len(a.Checklist) == 0 {
			s.bad(fmt.Sprintf("%s · %s · %s · closed with no checklist", a.ID, a.Type, siClient(a.UCC)))
			continue
		}
		if gaps := checklistGaps(a); len(gaps) > 0 {
			s.bad(fmt.Sprintf("%s · %s · %s · %d points blank", a.ID, a.Type, siClient(a.UCC), len(gaps)))
		}
	}
	s.summary = fmt.Sprintf("%d closed CDSL, DP and MCX alerts; %d without the checklist worked.", s.checked, s.found)
	return s, nil
}

func siAlertSample(e *siEnv) (siOut, error) {
	q := lastEndedQuarter(e.date)
	_, end, err := survQuarter(q)
	if err != nil {
		return siOut{}, err
	}
	if due := addDays(end, 15); e.date <= due {
		return siSkip(fmt.Sprintf("The sample of %s's closures is due by %s.", q, due))
	}
	var s siOut
	s.input("Alerts closed in "+q, end)
	v, err := e.o.alertSample(e.ctx, e.tenant, q)
	if err != nil {
		return s, err
	}
	ids := []string{}
	for _, a := range v.Sample {
		s.checked++
		if a.Sample == nil || a.Sample.Quarter != q {
			ids = append(ids, a.ID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		s.bad(fmt.Sprintf("%s · closed in %s · not checked", id, q))
	}
	s.summary = fmt.Sprintf("%d alerts closed in %s; %d in the sample; %d not checked.", v.Closed, q, v.Size, s.found)
	return s, nil
}
