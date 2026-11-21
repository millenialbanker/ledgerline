package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

var hrRouted sync.Map // *Server -> registered

// hrRoutes: the HRMS and Payroll modules (internal/ops/hrms.go and the
// files beside it) and the module switches. With a module off its routes
// answer 404 (ops.ErrModuleOff). Self-service and team routes are open to
// branch staff and authorised persons' logins too (their own records and
// their team only; the server decides).
func (s *Server) hrRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := hrRouted.LoadOrStore(s, true); dup {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	B := func(p string, h http.HandlerFunc, m bool) { scopedRoutes[p] = true; s.route(p, h, m) }
	B("GET /v1/modules", s.modules, false)
	R("PUT /v1/modules", s.putModules, true)
	// People.
	B("GET /v1/hr/staff", s.hrStaff, false)
	R("POST /v1/hr/staff", s.hrSave, true)
	B("GET /v1/hr/staff/{code}", s.hrGet, false)
	R("PUT /v1/hr/staff/{code}", s.hrSave, true)
	R("POST /v1/hr/staff/{code}/reveal", s.hrReveal, true)
	R("POST /v1/hr/staff/{code}/user", s.hrLinkUser, true)
	R("POST /v1/hr/staff/{code}/home-dealing", s.hrHomeDealing, true)
	R("POST /v1/hr/staff/{code}/documents", s.hrAddDoc, true)
	R("GET /v1/hr/staff/{code}/documents/{id}", s.hrDocFile, false)
	R("POST /v1/hr/staff/{code}/certs", s.hrCert, true)
	B("GET /v1/hr/staff/{code}/attendance", s.hrPersonAttendance, false)
	B("POST /v1/hr/staff/{code}/attendance", s.hrMark, true)
	B("GET /v1/hr/staff/{code}/balances", s.hrBalances, false)
	R("POST /v1/hr/staff/{code}/leaving", s.hrLeaving, true)
	R("POST /v1/hr/staff/{code}/leaving/approve", s.hrLeavingApprove, true)
	R("POST /v1/hr/staff/{code}/leaving/withdraw", s.hrLeavingWithdraw, true)
	R("POST /v1/hr/staff/{code}/device/approve", s.hrDeviceApprove, true)
	R("POST /v1/hr/staff/{code}/pin/unlock", s.hrPINUnlock, true)
	R("POST /v1/hr/staff/{code}/leave-adjust", s.hrLeaveAdjust, true)
	B("GET /v1/hr/org", s.hrOrg, false)
	B("GET /v1/hr/policy", s.hrPolicy, false)
	R("PUT /v1/hr/policy", s.hrPutPolicy, true)
	R("GET /v1/hr/checklists", s.hrChecklists, false)
	R("POST /v1/hr/checklists/{id}/items/{key}", s.hrCheckItem, true)
	R("GET /v1/hr/leavers", s.hrLeavers, false)
	R("GET /v1/hr/certificates", s.hrCerts, false)
	R("GET /v1/hr/kmp", s.hrKMP, false)
	// Own trading.
	B("GET /v1/hr/own-trading", s.hrOwnTrading, false)
	B("POST /v1/hr/own-trading/accounts", s.hrDeclareAccount, true)
	B("POST /v1/hr/own-trading/holdings", s.hrDeclareHoldings, true)
	B("POST /v1/hr/own-trading/pre-clearance", s.hrPreClear, true)
	R("POST /v1/hr/own-trading/pre-clearance/{id}", s.hrPreClearDecide, true)
	// Attendance: the phone app, the console, the kiosk and files.
	B("POST /v1/hr/attendance/punch", s.hrPunch, true)
	R("POST /v1/hr/attendance/import", s.hrImport, true)
	B("GET /v1/hr/attendance/report", s.hrWorkReport, false)
	R("GET /v1/hr/devices", s.hrDevices, false)
	B("GET /v1/hr/photos/{id}", s.hrPhoto, false)
	B("POST /v1/hr/wfh", s.hrWFH, true)
	B("POST /v1/hr/wfh/{id}", s.hrWFHDecide, true)
	R("GET /v1/hr/kiosks", s.hrKiosks, false)
	R("POST /v1/hr/kiosks", s.hrKioskNew, true)
	R("POST /v1/hr/kiosks/{id}/revoke", s.hrKioskRevoke, true)
	s.mux.HandleFunc("POST /kiosk-api/{tenant}/pair", s.kioskPair)
	s.mux.HandleFunc("POST /kiosk-api/{tenant}/punch", s.kioskPunch)
	// Leave.
	B("GET /v1/hr/leave", s.hrMyLeave, false)
	B("POST /v1/hr/leave", s.hrApplyLeave, true)
	B("POST /v1/hr/leave/{id}", s.hrDecideLeave, true)
	B("POST /v1/hr/leave/{id}/cancel", s.hrCancelLeave, true)
	B("GET /v1/hr/team", s.hrTeam, false)
	B("GET /v1/hr/on-leave", s.hrOnLeave, false)
	R("POST /v1/hr/leave-year/close", s.hrCloseYear, true)
	// Self-service.
	B("GET /v1/hr/me", s.hrMe, false)
	B("POST /v1/hr/me/pin", s.hrMyPIN, true)
	B("POST /v1/hr/me/certs", s.hrMyCert, true)
	B("POST /v1/hr/me/documents", s.hrMyDoc, true)
	// Payroll.
	R("GET /v1/payroll", s.payOverview, false)
	R("PUT /v1/payroll/settings", s.paySettings, true)
	R("GET /v1/payroll/statutory", s.payStatutory, false)
	R("POST /v1/payroll/statutory", s.paySaveStatutory, true)
	R("POST /v1/payroll/structures", s.payStructure, true)
	R("GET /v1/payroll/structures/standard", s.payStandard, false)
	R("POST /v1/payroll/inputs", s.payInput, true)
	R("GET /v1/payroll/runs/{month}", s.payRun, false)
	R("POST /v1/payroll/runs/{month}/{step}", s.payRunStep, true)
	R("GET /v1/payroll/runs/{month}/cbx", s.payCBX, false)
	R("GET /v1/payroll/runs/{month}/files/{kind}", s.payFile, false)
	R("GET /v1/payroll/runs/{month}/pt", s.payPT, false)
	B("GET /v1/payroll/runs/{month}/payslips/{code}", s.payPayslip, false)
	R("GET /v1/payroll/form138", s.payForm138, false)
	B("GET /v1/payroll/form130/{code}", s.payForm130, false)
	R("POST /v1/payroll/fnf/{code}", s.payFnF, true)
	B("POST /v1/payroll/claims", s.payClaim, true)
	B("POST /v1/payroll/claims/{id}", s.payClaimDecide, true)
	R("POST /v1/payroll/loans", s.payLoan, true)
	R("POST /v1/payroll/loans/{id}/approve", s.payLoanApprove, true)
	B("POST /v1/payroll/tax-declarations", s.payTaxDecl, true)
	B("POST /v1/payroll/tax-declarations/proofs", s.payTaxProof, true)
	R("POST /v1/payroll/tax-declarations/verify", s.payTaxVerify, true)
}

// ---- modules ----

func (s *Server) modules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.Modules(r.Context(), tenant(r)))
}

func (s *Server) putModules(w http.ResponseWriter, r *http.Request) {
	var b ops.ModuleSwitches
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetModules(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// ---- people ----

func (s *Server) hrStaff(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Staff(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.GetEmployee(r.Context(), tenant(r), actor(r), r.PathValue("code"))
	ok(w, v, err)
}

func (s *Server) hrSave(w http.ResponseWriter, r *http.Request) {
	var b ops.Employee
	if !decode(w, r, &b) {
		return
	}
	if c := r.PathValue("code"); c != "" {
		b.Code = c
	} else if b.Code != "" {
		if _, err := s.ops.GetEmployee(r.Context(), tenant(r), actor(r), b.Code); err == nil {
			writeErr(w, 409, "exists", "Employee "+b.Code+" exists; change it with PUT /v1/hr/staff/{code}.", nil)
			return
		}
	}
	v, err := s.ops.SaveEmployee(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) hrReveal(w http.ResponseWriter, r *http.Request) {
	var b struct{ Field, Reason string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RevealEmployee(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.Field, b.Reason)
	ok(w, map[string]string{"value": v}, err)
}

func (s *Server) hrLinkUser(w http.ResponseWriter, r *http.Request) {
	var b struct{ User string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.LinkUser(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.User)
	ok(w, v, err)
}

func (s *Server) hrHomeDealing(w http.ResponseWriter, r *http.Request) {
	var b ops.HomeDealing
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetHomeDealing(r.Context(), tenant(r), actor(r), r.PathValue("code"), b)
	ok(w, v, err)
}

func readBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		writeErr(w, 413, "too_large", "The file is too large.", nil)
		return nil, false
	}
	return data, true
}

func (s *Server) hrAddDoc(w http.ResponseWriter, r *http.Request) {
	data, okb := readBody(w, r, ops.MaxDocBytes+1)
	if !okb {
		return
	}
	q := r.URL.Query()
	v, err := s.ops.AddStaffDoc(r.Context(), tenant(r), actor(r), r.PathValue("code"), q.Get("kind"), q.Get("name"), q.Get("expiry"), data)
	ok(w, v, err)
}

func (s *Server) hrMyDoc(w http.ResponseWriter, r *http.Request) {
	data, okb := readBody(w, r, ops.MaxDocBytes+1)
	if !okb {
		return
	}
	v, _ := s.ops.SelfService(r.Context(), tenant(r), actor(r), "")
	if !v.Linked {
		writeErr(w, 403, "forbidden", "Your login is not linked to an employee.", nil)
		return
	}
	q := r.URL.Query()
	d, err := s.ops.AddStaffDoc(r.Context(), tenant(r), actor(r), v.Profile.Code, q.Get("kind"), q.Get("name"), q.Get("expiry"), data)
	ok(w, d, err)
}

func (s *Server) hrDocFile(w http.ResponseWriter, r *http.Request) {
	d, b, err := s.ops.StaffDocFile(r.Context(), tenant(r), actor(r), r.PathValue("code"), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, d.Name, d.ContentType, b)
}

func (s *Server) hrCert(w http.ResponseWriter, r *http.Request) {
	var b ops.StaffCert
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveStaffCert(r.Context(), tenant(r), actor(r), r.PathValue("code"), b)
	ok(w, v, err)
}

func (s *Server) hrMyCert(w http.ResponseWriter, r *http.Request) {
	var b ops.StaffCert
	if !decode(w, r, &b) {
		return
	}
	me, _ := s.ops.SelfService(r.Context(), tenant(r), actor(r), "")
	if !me.Linked {
		writeErr(w, 403, "forbidden", "Your login is not linked to an employee.", nil)
		return
	}
	v, err := s.ops.SaveStaffCert(r.Context(), tenant(r), actor(r), me.Profile.Code, b)
	ok(w, v, err)
}

func (s *Server) hrPersonAttendance(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PersonAttendance(r.Context(), tenant(r), actor(r), r.PathValue("code"), r.URL.Query().Get("month"))
	ok(w, v, err)
}

func (s *Server) hrMark(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, Status, In, Out, Mode, Note string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkAttendance(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.Date, b.Status, b.In, b.Out, b.Mode, b.Note)
	ok(w, v, err)
}

func (s *Server) hrBalances(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EmployeeBalances(r.Context(), tenant(r), actor(r), r.PathValue("code"), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) hrLeaving(w http.ResponseWriter, r *http.Request) {
	var b struct {
		LastDay string `json:"last_day"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.StartLeaving(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.LastDay, b.Reason)
	ok(w, v, err)
}

func (s *Server) hrLeavingApprove(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ApproveLeaving(r.Context(), tenant(r), actor(r), r.PathValue("code"))
	ok(w, v, err)
}

func (s *Server) hrLeavingWithdraw(w http.ResponseWriter, r *http.Request) {
	var b struct{ Note string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.WithdrawLeaving(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.Note)
	ok(w, v, err)
}

func (s *Server) hrDeviceApprove(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ApproveDevice(r.Context(), tenant(r), actor(r), r.PathValue("code"))
	ok(w, v, err)
}

func (s *Server) hrPINUnlock(w http.ResponseWriter, r *http.Request) {
	err := s.ops.UnlockKioskPIN(r.Context(), tenant(r), actor(r), r.PathValue("code"))
	ok(w, map[string]bool{"ok": err == nil}, err)
}

func (s *Server) hrLeaveAdjust(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Type string  `json:"type"`
		Days float64 `json:"days"`
		Note string  `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AdjustLeave(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.Type, b.Days, b.Note)
	ok(w, v, err)
}

func (s *Server) hrOrg(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.OrgChart(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrPolicy(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.HRPolicy(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrPutPolicy(w http.ResponseWriter, r *http.Request) {
	var b ops.HRPolicy
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveHRPolicy(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) hrChecklists(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Checklists(r.Context(), tenant(r), actor(r), r.URL.Query().Get("all") == "1")
	ok(w, v, err)
}

func (s *Server) hrCheckItem(w http.ResponseWriter, r *http.Request) {
	var b struct {
		NA   bool   `json:"na"`
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DoCheckItem(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("key"), b.NA, b.Note)
	ok(w, v, err)
}

func (s *Server) hrLeavers(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Leavers(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrCerts(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StaffCertRegister(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrKMP(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.KMPList(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// ---- own trading ----

func (s *Server) hrOwnTrading(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.OwnTrading(r.Context(), tenant(r), actor(r), r.URL.Query().Get("self") == "1")
	ok(w, v, err)
}

func (s *Server) hrDeclareAccount(w http.ResponseWriter, r *http.Request) {
	var b ops.StaffAccount
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DeclareAccount(r.Context(), tenant(r), actor(r), b.Employee, b)
	ok(w, v, err)
}

func (s *Server) hrDeclareHoldings(w http.ResponseWriter, r *http.Request) {
	var b ops.HoldingsDecl
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DeclareHoldings(r.Context(), tenant(r), actor(r), b.Employee, b)
	ok(w, v, err)
}

func (s *Server) hrPreClear(w http.ResponseWriter, r *http.Request) {
	var b ops.PreClearance
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestPreClearance(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) hrPreClearDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecidePreClearance(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

// ---- attendance ----

// hrPunch is a punch from the phone app (device_id set; the app has
// unlocked with the phone's own biometric) or the console. The client's
// address decides an office punch on the office network.
func (s *Server) hrPunch(w http.ResponseWriter, r *http.Request) {
	var b ops.PunchReq
	if !decodeMax(w, r, &b, 4<<20) {
		return
	}
	b.IP = clientIP(r)
	v, err := s.ops.PunchAttendance(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// decodeMax is decode with a larger limit (a punch may carry a photo).
func decodeMax(w http.ResponseWriter, r *http.Request, v any, max int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	return decode(w, r, v)
}

func (s *Server) hrImport(w http.ResponseWriter, r *http.Request) {
	data, okb := readBody(w, r, 10<<20)
	if !okb {
		return
	}
	v, err := s.ops.ImportAttendanceCSV(r.Context(), tenant(r), actor(r), data)
	ok(w, v, err)
}

func (s *Server) hrWorkReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.WorkReport(r.Context(), tenant(r), actor(r), q.Get("month"), q.Get("raise") == "1")
	ok(w, v, err)
}

func (s *Server) hrDevices(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Devices(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrPhoto(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.PunchPhotoFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	ct := "image/jpeg"
	if len(b) > 4 && string(b[1:4]) == "PNG" {
		ct = "image/png"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", ct)
	w.Write(b)
}

func (s *Server) hrWFH(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, Reason string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestWFH(r.Context(), tenant(r), actor(r), b.Date, b.Reason)
	ok(w, v, err)
}

func (s *Server) hrWFHDecide(w http.ResponseWriter, r *http.Request) {
	var b struct{ Approve bool }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideWFH(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve)
	ok(w, v, err)
}

func (s *Server) hrKiosks(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Kiosks(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrKioskNew(w http.ResponseWriter, r *http.Request) {
	var b struct{ Name, Branch string }
	if !decode(w, r, &b) {
		return
	}
	k, code, err := s.ops.CreateKiosk(r.Context(), tenant(r), actor(r), b.Name, b.Branch)
	ok(w, map[string]any{"kiosk": k, "pairing_code": code}, err)
}

func (s *Server) hrKioskRevoke(w http.ResponseWriter, r *http.Request) {
	err := s.ops.RevokeKiosk(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"ok": err == nil}, err)
}

// kioskPair and kioskPunch are the tablet kiosk's calls (/kiosk/<firm>):
// no sign-in; the pairing code once, then the kiosk's token, and always
// the office network (checked in ops).
func (s *Server) kioskPair(w http.ResponseWriter, r *http.Request) {
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	t := r.PathValue("tenant")
	if s.ops.FirmClosed(t) {
		writeClosed(w)
		return
	}
	if !s.loginLimit.allow("kiosk-pair|" + clientIP(r)) {
		writeErr(w, 429, "too_many", "Too many tries; wait a few minutes.", nil)
		return
	}
	k, tok, err := s.ops.PairKiosk(r.Context(), t, b.Code, clientIP(r))
	ok(w, map[string]any{"kiosk": k, "token": tok}, err)
}

func (s *Server) kioskPunch(w http.ResponseWriter, r *http.Request) {
	var b ops.KioskPunchReq
	if !decodeMax(w, r, &b, 4<<20) {
		return
	}
	t := r.PathValue("tenant")
	if s.ops.FirmClosed(t) {
		writeClosed(w)
		return
	}
	v, err := s.ops.KioskPunch(r.Context(), t, r.Header.Get("X-Kiosk-Token"), clientIP(r), b)
	ok(w, v, err)
}

// ---- leave ----

func (s *Server) hrMyLeave(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MyLeave(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrApplyLeave(w http.ResponseWriter, r *http.Request) {
	var b ops.LeaveRequest
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ApplyLeave(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) hrDecideLeave(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideLeave(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) hrCancelLeave(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CancelLeave(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) hrTeam(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Team(r.Context(), tenant(r), actor(r), r.URL.Query().Get("month"))
	ok(w, v, err)
}

func (s *Server) hrOnLeave(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.OnLeave(r.Context(), tenant(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) hrCloseYear(w http.ResponseWriter, r *http.Request) {
	var b struct {
		YearEnd string `json:"year_end"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CloseLeaveYear(r.Context(), tenant(r), actor(r), b.YearEnd)
	ok(w, v, err)
}

func (s *Server) hrMe(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SelfService(r.Context(), tenant(r), actor(r), r.URL.Query().Get("month"))
	ok(w, v, err)
}

func (s *Server) hrMyPIN(w http.ResponseWriter, r *http.Request) {
	var b struct{ PIN string }
	if !decode(w, r, &b) {
		return
	}
	err := s.ops.SetKioskPIN(r.Context(), tenant(r), actor(r), b.PIN)
	ok(w, map[string]bool{"ok": err == nil}, err)
}

// ---- payroll ----

func (s *Server) payOverview(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Payroll(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) paySettings(w http.ResponseWriter, r *http.Request) {
	var b ops.PaySettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePaySettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) payStatutory(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StatutoryTables(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) paySaveStatutory(w http.ResponseWriter, r *http.Request) {
	var b ops.Statutory
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveStatutory(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) payStructure(w http.ResponseWriter, r *http.Request) {
	var b ops.SalaryStructure
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveSalaryStructure(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) payStandard(w http.ResponseWriter, r *http.Request) {
	g, err := money.ParseAmount(r.URL.Query().Get("gross"))
	if err != nil || g <= 0 {
		writeErr(w, 400, "bad_gross", "gross must be a monthly amount in rupees", nil)
		return
	}
	if !s.allowed(w, r, "Setting salary structures", ops.RolePayroll, ops.RoleHR) {
		return
	}
	writeJSON(w, 200, ops.StandardStructure(g))
}

func (s *Server) payInput(w http.ResponseWriter, r *http.Request) {
	var b ops.PayInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePayInput(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) payRun(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Payrun(r.Context(), tenant(r), actor(r), r.PathValue("month"))
	ok(w, v, err)
}

func (s *Server) payRunStep(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
		Date   string `json:"date"`
		Ref    string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	ctx, t, a, m := r.Context(), tenant(r), actor(r), r.PathValue("month")
	var v any
	var err error
	switch r.PathValue("step") {
	case "make":
		v, err = s.ops.MakePayrun(ctx, t, a, m)
	case "submit":
		v, err = s.ops.SubmitPayrun(ctx, t, a, m)
	case "return":
		v, err = s.ops.ReturnPayrun(ctx, t, a, m, b.Reason)
	case "approve":
		v, err = s.ops.ApprovePayrun(ctx, t, a, m)
	case "paid":
		v, err = s.ops.MarkPayrunPaid(ctx, t, a, m, b.Date, b.Ref)
	case "email":
		v, err = s.ops.EmailPayslips(ctx, t, a, m)
	case "cbx":
		v, err = s.ops.SendSalaryHDFC(ctx, t, a, m)
	case "cbx-check":
		v, err = s.ops.CheckSalaryHDFC(ctx, t, a, m)
	default:
		writeErr(w, 404, "not_found", "Unknown step.", nil)
		return
	}
	ok(w, v, err)
}

func (s *Server) payCBX(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SalarySendView(r.Context(), tenant(r), actor(r), r.PathValue("month"))
	ok(w, v, err)
}

func (s *Server) payFile(w http.ResponseWriter, r *http.Request) {
	ctx, t, a, m := r.Context(), tenant(r), actor(r), r.PathValue("month")
	var b []byte
	var err error
	name, ct := "", "text/csv; charset=utf-8"
	switch r.PathValue("kind") {
	case "register":
		b, err = s.ops.SalaryRegisterCSV(ctx, t, a, m)
		name = "salary-register-" + m + ".csv"
	case "bank":
		b, err = s.ops.BankFileCSV(ctx, t, a, m)
		name = "salary-bank-" + m + ".csv"
	case "ecr":
		b, err = s.ops.PFECR(ctx, t, a, m)
		name, ct = "ECR-"+m+".txt", "text/plain; charset=utf-8"
	case "esi":
		b, err = s.ops.ESIFile(ctx, t, a, m)
		name = "ESI-" + m + ".csv"
	default:
		writeErr(w, 404, "not_found", "Files: register, bank, ecr, esi.", nil)
		return
	}
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, ct, b)
}

func (s *Server) payPT(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PTSummary(r.Context(), tenant(r), actor(r), r.PathValue("month"))
	ok(w, v, err)
}

func (s *Server) payPayslip(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSuffix(r.PathValue("code"), ".pdf")
	b, err := s.ops.PayslipPDF(r.Context(), tenant(r), actor(r), r.PathValue("month"), code)
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "payslip-"+r.PathValue("month")+"-"+code+".pdf", "application/pdf", b)
}

func (s *Server) payForm138(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	qn, _ := strconv.Atoi(q.Get("quarter"))
	b, err := s.ops.Form138CSV(r.Context(), tenant(r), actor(r), q.Get("fy"), qn, q.Get("annexure") == "2")
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "form138-"+q.Get("fy")+"-Q"+q.Get("quarter")+map[bool]string{true: "-annexure2"}[q.Get("annexure") == "2"]+".csv", "text/csv; charset=utf-8", b)
}

func (s *Server) payForm130(w http.ResponseWriter, r *http.Request) {
	fy := r.URL.Query().Get("fy")
	b, err := s.ops.Form130PartB(r.Context(), tenant(r), actor(r), r.PathValue("code"), fy)
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "form130-partB-"+fy+"-"+r.PathValue("code")+".pdf", "application/pdf", b)
}

func (s *Server) payFnF(w http.ResponseWriter, r *http.Request) {
	var b struct {
		NoticeRecovery money.Amount `json:"notice_recovery"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.FullAndFinal(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.NoticeRecovery)
	ok(w, v, err)
}

func (s *Server) payClaim(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.Claim
		Bill     []byte `json:"bill_file"`
		BillName string `json:"bill_name"`
	}
	if !decodeMax(w, r, &b, 8<<20) {
		return
	}
	v, err := s.ops.ClaimReimbursement(r.Context(), tenant(r), actor(r), b.Claim, b.Bill, b.BillName)
	ok(w, v, err)
}

func (s *Server) payClaimDecide(w http.ResponseWriter, r *http.Request) {
	var b struct{ Approve bool }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideClaim(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve)
	ok(w, v, err)
}

func (s *Server) payLoan(w http.ResponseWriter, r *http.Request) {
	var b ops.Loan
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.GiveAdvance(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) payLoanApprove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PaidOn string `json:"paid_on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ApproveAdvance(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.PaidOn)
	ok(w, v, err)
}

func (s *Server) payTaxDecl(w http.ResponseWriter, r *http.Request) {
	var b ops.TaxDecl
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveTaxDecl(r.Context(), tenant(r), actor(r), b.Employee, b)
	ok(w, v, err)
}

func (s *Server) payTaxProof(w http.ResponseWriter, r *http.Request) {
	data, okb := readBody(w, r, ops.MaxDocBytes+1)
	if !okb {
		return
	}
	q := r.URL.Query()
	v, err := s.ops.AddTaxProof(r.Context(), tenant(r), actor(r), q.Get("employee"), q.Get("fy"), q.Get("name"), data)
	ok(w, v, err)
}

func (s *Server) payTaxVerify(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Employee string         `json:"employee"`
		FY       string         `json:"fy"`
		Verified ops.TaxAmounts `json:"verified"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.VerifyTaxDecl(r.Context(), tenant(r), actor(r), b.Employee, b.FY, b.Verified)
	ok(w, v, err)
}
