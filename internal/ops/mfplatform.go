package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- exchange MF platforms ----
//
// Every platform call goes through mfPlatform. Each platform has its own
// connection (Settings → Connections) holding the firm's logins; nothing
// here holds a key. The connectors are built only from the platforms'
// public specifications:
//
//   - BSE StAR MF, "API File Structure" v3.5 (https://www.bsestarmf.in/APIFileStructure.pdf):
//     mfbse.go.
//   - NSE MF Invest, "NSEMF API Details" v1.9.7
//     (https://nseinvestuat.nseindia.com/nsemfdesk/resources/upload/apidetails/NSEMF_API_Details_V1.9.7.pdf):
//     mfnse.go.
//
// Only the parts of those documents that could be read are implemented.
// A function whose section could not be read returns errMFNoAPI: staff do
// that step on the platform's member portal and record the result here,
// or load the platform's report file (MF imports). None of this has been
// run against a live platform; the tests use fake servers answering in
// the documented formats.

// errMFNoAPI marks a platform function that is not called through the
// API here.
var errMFNoAPI = errors.New("not available through the platform's API in Ledgerline")

func mfNoAPI(platform, what, why string) error {
	return fmt.Errorf("%w: %w: %s %s: %s", ErrState, errMFNoAPI, MFPlatformNames[platform][0], what, why)
}

// mfConn is a platform connection as stored, with its secrets.
type mfConn struct {
	Platform   string
	Base       string // BSE: the order entry service address; NSE: https://host
	Member     string
	User       string
	Password   string // BSE
	PassKey    string // BSE
	Secret     string // NSE API secret
	LicenseKey string // NSE API member license key
	IP         string // the server's address, which BSE takes on each order
	Live       bool
	Fetch      bool
}

var mfConnIDs = map[string]string{MFBSE: connBSEStarMF, MFNSE: connNSEMFInvest}

func (o *Ops) mfConn(ctx context.Context, tenant, platform string) (mfConn, error) {
	id := mfConnIDs[platform]
	if id == "" {
		return mfConn{}, invalid("unknown platform %q", platform)
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if err != nil || c.Status == "Not set" {
		return mfConn{}, fmt.Errorf("%w: set up %s in Settings → Connections", ErrState, MFPlatformNames[platform][0])
	}
	cn := mfConn{Platform: platform, Member: strings.TrimSpace(c.Values["member"]), User: strings.TrimSpace(c.Values["user"]), Live: c.Values["env"] == "Live",
		Fetch: c.Values["fetch"] == "On", IP: firstOf(os.Getenv("LEDGERLINE_SERVER_IP"), "127.0.0.1")}
	switch platform {
	case MFBSE:
		cn.Password, cn.PassKey = c.Secrets["password"], c.Secrets["passkey"]
		cn.Base = bseStarDemo
		if u := strings.TrimSpace(c.Values["url"]); u != "" {
			cn.Base = u
		} else if cn.Live {
			return cn, invalid("BSE gives the live order entry address after the demo and activation: enter it on the BSE StAR MF connection")
		}
		if cn.Member == "" || cn.User == "" || cn.Password == "" || cn.PassKey == "" {
			return cn, invalid("the BSE StAR MF connection needs the member ID, user ID, password and pass key")
		}
	case MFNSE:
		cn.Secret, cn.LicenseKey = c.Secrets["secret"], c.Secrets["license_key"]
		cn.Base = nseMFUAT
		if cn.Live {
			cn.Base = nseMFLive
		}
		if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
			cn.Base = u
		}
		if cn.Member == "" || cn.User == "" || cn.Secret == "" || cn.LicenseKey == "" {
			return cn, invalid("the NSE MF Invest connection needs the member code, login user ID, API secret and API member license key")
		}
	}
	return cn, nil
}

// mfOrderReq is a purchase, redemption or switch for a platform.
type mfOrderReq struct {
	Ref          string
	ClientCode   string
	SchemeCode   string
	ToSchemeCode string
	Type         string // Purchase, Redemption, Switch
	BuyType      string // FRESH, ADDITIONAL
	DPTxn        string // C (CDSL), N (NSDL), P (physical)
	Amount       money.Amount
	Units        money.Dec
	AllUnits     bool
	Folio        string
	EUIN         string
	Mobile       string
	Email        string
	BankAccount  string // where redemption proceeds go (the client's own account)
	MandateID    string // the platform's mandate number, for a purchase paid by mandate
	Remarks      string
}

// mfSIPReq is an XSIP registration or cancellation.
type mfSIPReq struct {
	Ref          string
	ClientCode   string
	SchemeCode   string
	AMCCode      string
	DPTxn        string
	StartDate    string // YYYY-MM-DD
	Frequency    string // MONTHLY, QUARTERLY, WEEKLY
	Amount       money.Amount
	Installments int
	Folio        string
	EUIN         string
	MandateID    string // the platform's mandate number
	RegID        string // for a cancellation
	FirstOrder   bool
	Mobile       string
	Email        string
	Remarks      string
}

// mfMandateReq asks a platform to register a mandate.
type mfMandateReq struct {
	ClientCode string
	Amount     money.Amount
	Type       string
	Account    string
	IFSC       string
	From, Till string
}

// mfReply is a platform's answer to one request.
type mfReply struct {
	OK      bool
	OrderNo string
	RegID   string
	Status  string
	Remark  string
}

type mfStatusRow struct{ OrderNo, Status, Remark string }
type mfAllotRow struct{ OrderNo, Units, NAV, Amount, Date, Folio string }
type mfMandateRow struct{ MandateID, UMRN, Status, Remark string }

// mfPlatform is one exchange platform's API.
type mfPlatform interface {
	RegisterClient(ctx context.Context, cn mfConn, c Client, p MFProfile) (mfReply, error)
	Order(ctx context.Context, cn mfConn, r mfOrderReq) (mfReply, error)
	Switch(ctx context.Context, cn mfConn, r mfOrderReq) (mfReply, error)
	CancelOrder(ctx context.Context, cn mfConn, ucc, orderNo, remark string) (mfReply, error)
	RegisterSIP(ctx context.Context, cn mfConn, r mfSIPReq) (mfReply, error)
	CancelSIP(ctx context.Context, cn mfConn, r mfSIPReq) (mfReply, error)
	RegisterMandate(ctx context.Context, cn mfConn, r mfMandateReq) (mfReply, error)
	MandateStatus(ctx context.Context, cn mfConn, from, to string) ([]mfMandateRow, error)
	OrderStatus(ctx context.Context, cn mfConn, from, to string) ([]mfStatusRow, error)
	Allotments(ctx context.Context, cn mfConn, from, to string) ([]mfAllotRow, error)
}

// mfPlatforms are the connectors; tests may replace one.
var mfPlatforms = map[string]mfPlatform{MFBSE: bseStarMF{}, MFNSE: nseMFInvest{}}

func mfPlatformFor(p string) mfPlatform { return mfPlatforms[p] }

// ---- client registration on a platform ----

// mfRegCheck lists what a platform's client registration needs that the
// profile lacks.
func mfRegCheck(c Client, p MFProfile) []string {
	var miss []string
	ind := p.TaxStatus == "01"
	for _, x := range []struct {
		bad  bool
		what string
	}{
		{p.TaxStatus == "", "tax status"}, {p.Occupation == "", "occupation"}, {ind && p.Gender == "", "gender"},
		{c.DOB == "", "date of birth"}, {c.PAN == "", "PAN"}, {c.Email == "", "email"}, {c.Mobile == "", "mobile"},
		{p.Address == "", "address"}, {c.City == "", "city"}, {p.StateCode == "", "state code"}, {p.Pincode == "", "PIN code"},
		{c.Bank.Account == "" || c.Bank.IFSC == "", "bank account"}, {!p.FATCA, "FATCA self-certification"},
		{ind && !p.NomOptOut && len(p.Nominees) == 0, "nomination or opt-out"},
	} {
		if x.bad {
			miss = append(miss, x.what)
		}
	}
	return miss
}

// RegisterMFClient sends the client's registration (UCC) to a platform.
// A platform that takes it through the API answers at once; the investor
// then authorises it (NSE sends the link), after which staff mark it
// Registered. For a platform without the API here, staff register on
// the platform's own site and mark it here.
func (o *Ops) RegisterMFClient(ctx context.Context, tenant, actor, ucc, platform string) (MFProfile, error) {
	if err := o.require(ctx, tenant, actor, "Registering clients on MF platforms", RoleMaker, RoleChecker); err != nil {
		return MFProfile{}, err
	}
	platform = strings.ToUpper(platform)
	set, err := o.MFSettingsGet(ctx, tenant)
	if err != nil {
		return MFProfile{}, err
	}
	if !set.enabled(platform) {
		return MFProfile{}, invalid("platform %q is not enabled in MF settings", platform)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return MFProfile{}, err
	}
	if c.Status != "Active" || mfKYCBlocked(c.KRAStatus) {
		return MFProfile{}, invalid("%s must be active with KYC validated at the KRA", c.UCC)
	}
	p, err := o.MFProfile(ctx, tenant, c.UCC)
	if err != nil {
		return p, err
	}
	if miss := mfRegCheck(c, p); len(miss) > 0 {
		return p, invalid("the MF profile lacks: %s", strings.Join(miss, ", "))
	}
	cn, err := o.mfConn(ctx, tenant, platform)
	if err != nil {
		return p, err
	}
	rep, err := mfPlatformFor(platform).RegisterClient(ctx, cn, c, p)
	if err != nil {
		return p, err
	}
	reg := MFPlatformReg{Status: "Submitted", At: o.now().Format(time.RFC3339), By: actor, Remark: firstOf(rep.Remark, "awaiting the investor's authorisation on the platform")}
	if !rep.OK {
		reg.Status = "Failed"
	}
	if err := o.setMFReg(ctx, tenant, actor, c.UCC, platform, reg); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "mf.client.registration", c.UCC, map[string]any{"platform": platform, "status": reg.Status, "remark": reg.Remark})
	if !rep.OK {
		return p, fmt.Errorf("%w: %s refused the registration: %s", ErrState, MFPlatformNames[platform][0], firstOf(rep.Remark, "no reason given"))
	}
	return o.MFProfile(ctx, tenant, c.UCC)
}

// MarkMFRegistration records a client's registration status on a
// platform as the platform shows it: Registered (authorised and active),
// Submitted or Failed.
func (o *Ops) MarkMFRegistration(ctx context.Context, tenant, actor, ucc, platform, status, remark string) (MFProfile, error) {
	if err := o.require(ctx, tenant, actor, "Registering clients on MF platforms", RoleMaker, RoleChecker); err != nil {
		return MFProfile{}, err
	}
	platform = strings.ToUpper(platform)
	if _, ok := MFPlatformNames[platform]; !ok {
		return MFProfile{}, invalid("platform is BSE or NSE")
	}
	if !contains([]string{"Registered", "Submitted", "Failed"}, status) {
		return MFProfile{}, invalid("status is Registered, Submitted or Failed")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return MFProfile{}, err
	}
	if status == "Registered" {
		p, _ := o.MFProfile(ctx, tenant, c.UCC)
		if !p.FATCA {
			return p, invalid("record the FATCA self-certification first")
		}
	}
	if err := o.setMFReg(ctx, tenant, actor, c.UCC, platform, MFPlatformReg{Status: status, At: o.now().Format(time.RFC3339), By: actor, Remark: remark}); err != nil {
		return MFProfile{}, err
	}
	o.log(ctx, tenant, actor, "mf.client.registration", c.UCC, map[string]any{"platform": platform, "status": status, "remark": remark, "manual": true})
	return o.MFProfile(ctx, tenant, c.UCC)
}

func (o *Ops) setMFReg(ctx context.Context, tenant, actor, ucc, platform string, reg MFPlatformReg) error {
	p, err := o.MFProfile(ctx, tenant, ucc)
	if err != nil {
		return err
	}
	p.Platforms[platform] = reg
	return o.putMFProfile(ctx, tenant, actor, p)
}

// ---- applying what a platform reports ----

// mfApplyStatuses updates placed orders from status rows; it returns how
// many changed.
func (o *Ops) mfApplyStatuses(ctx context.Context, tenant, actor string, rows []mfStatusRow) (int, error) {
	idx, err := o.mfByOrderNo(ctx, tenant)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		id, ok := idx[strings.TrimSpace(r.OrderNo)]
		if !ok {
			continue
		}
		changed := false
		_, err := docs.Update(ctx, o.Docs, tenant, cMF, id, actor, func(x *MFOrder) error {
			st := mfStage(r.Status)
			if x.PlatformStat == r.Status || x.Status == MFAllotted || x.Status == MFRedeemed {
				return errAlreadyDone
			}
			x.PlatformStat = r.Status
			if st != "" {
				x.Status = st
			}
			if r.Remark != "" {
				x.Remark = r.Remark
			}
			changed = true
			return nil
		})
		if err != nil && !errors.Is(err, errAlreadyDone) {
			return n, err
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// mfApplyAllotments records allotments (or redemptions) the platform or
// RTA reports against placed orders.
func (o *Ops) mfApplyAllotments(ctx context.Context, tenant, actor, source string, rows []mfAllotRow) (int, []string, error) {
	idx, err := o.mfByOrderNo(ctx, tenant)
	if err != nil {
		return 0, nil, err
	}
	n := 0
	var skipped []string
	for _, r := range rows {
		id, ok := idx[strings.TrimSpace(r.OrderNo)]
		if !ok {
			skipped = append(skipped, r.OrderNo+": no order placed here")
			continue
		}
		_, err := o.recordAllotment(ctx, tenant, actor, id, MFAllotment{Units: r.Units, NAV: r.NAV, Date: r.Date, Folio: r.Folio, Amount: r.Amount}, source)
		switch {
		case errors.Is(err, ErrState):
			continue // already allotted
		case err != nil:
			skipped = append(skipped, r.OrderNo+": "+err.Error())
		default:
			n++
		}
	}
	return n, skipped, nil
}

// mfByOrderNo maps the platform's order numbers (and our own IDs) to our
// order IDs.
func (o *Ops) mfByOrderNo(ctx context.Context, tenant string) (map[string]string, error) {
	all, err := docs.List[MFOrder](ctx, o.Docs, tenant, cMF, "")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, x := range all {
		if x.Source == "" {
			continue
		}
		if x.OrderNo != "" {
			m[x.OrderNo] = x.ID
		}
		m[x.ID] = x.ID
	}
	return m, nil
}

// mfApplyMandates updates mandates from status rows (by the platform's
// mandate number or UMRN).
func (o *Ops) mfApplyMandates(ctx context.Context, tenant, actor string, rows []mfMandateRow) (int, error) {
	ps, err := docs.List[MFProfile](ctx, o.Docs, tenant, cMFProf, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range ps {
		changed := false
		for i, m := range p.Mandates {
			for _, r := range rows {
				if (r.MandateID == "" || r.MandateID != m.PlatformID) && (r.UMRN == "" || r.UMRN != m.UMRN) {
					continue
				}
				st := mfMandateStatus(r.Status)
				if st == "" || (st == m.Status && (r.UMRN == "" || r.UMRN == m.UMRN)) {
					continue
				}
				p.Mandates[i].Status, p.Mandates[i].Remark = st, firstOf(r.Remark, r.Status)
				if r.UMRN != "" {
					p.Mandates[i].UMRN = r.UMRN
				}
				changed = true
				n++
			}
		}
		if changed {
			if err := o.putMFProfile(ctx, tenant, actor, p); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// mfMandateStatus maps a platform's or bank's words to a mandate status.
func mfMandateStatus(s string) string {
	u := strings.ToUpper(s)
	switch {
	case strings.Contains(u, "REJECT"), strings.Contains(u, "FAIL"):
		return "Rejected"
	case strings.Contains(u, "CANCEL"):
		return "Cancelled"
	case strings.Contains(u, "APPROVED"), strings.Contains(u, "ACCEPT"), strings.Contains(u, "SUCCESS"), strings.Contains(u, "ACTIVE"), strings.Contains(u, "REGISTERED"):
		return "Approved"
	case strings.Contains(u, "PROCESS"), strings.Contains(u, "PENDING"), strings.Contains(u, "SUBMIT"):
		return "Pending"
	}
	return ""
}
