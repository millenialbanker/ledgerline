package ops

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/vault"
)

// Keys the broker holds. SEBI's framework for regulated entities adopting
// cloud services keeps "ownership of data, logs and encryption keys" with
// the regulated entity. Ledgerline encrypts every firm's data with keys of
// its own (internal/vault); a firm can instead hold the key itself, so
// Ledgerline can read the firm's data only while the firm lets it:
//
//   - AWS KMS in the firm's own AWS account (Mumbai or Hyderabad), reached
//     with an access key or a cross-account role entered in Settings →
//     Connections → Firm's own encryption key: AWS KMS;
//   - a key service the firm runs (a key file or an HSM behind HTTPS), for
//     firms not on AWS: Connections → Firm's own encryption key: firm's key
//     service.
//
// The Admin tests the key, moves the firm onto it and rotates it from the
// Encryption keys panel in Settings. When the firm's key refuses (disabled,
// access revoked) or cannot be reached, the firm's data is not served at
// all; an audit entry and a work item for the Admin are raised once per
// outage, and service resumes by itself when the key answers again. The
// vault's package comment explains the design.

const (
	connFirmKeyAWS    = "firmkey-aws"
	connFirmKeyRemote = "firmkey-remote"
	firmKeyProcess    = "firmkey"
)

func init() {
	ConnProviders = append(ConnProviders,
		ConnProvider{ID: connFirmKeyAWS, Name: "Firm's own encryption key: AWS KMS", Group: "Security", Test: "none",
			Purpose: "The KMS key in the firm's own AWS account that holds the firm's encryption key (Settings → Encryption keys). India regions only. Ledgerline calls only kms:Encrypt and kms:Decrypt, always with the firm code in the encryption context",
			Fields: []ConnField{
				{Key: "region", Label: "Region", Options: vault.KMSRegions, Optional: true},
				{Key: "key_arn", Label: "KMS key ARN", Placeholder: "arn:aws:kms:ap-south-1:111122223333:key/…"},
				{Key: "access_key_id", Label: "Access key ID"},
				{Key: "secret_access_key", Label: "Secret access key", Secret: true},
				{Key: "role_arn", Label: "Role to assume in the firm's account (cross-account; optional)", Optional: true, Placeholder: "arn:aws:iam::111122223333:role/…"},
				{Key: "external_id", Label: "External ID the role requires (optional)", Optional: true},
				{Key: "url", Label: "KMS address (leave empty for AWS's own)", Optional: true, Placeholder: "https://kms.ap-south-1.amazonaws.com"},
				{Key: "sts_url", Label: "STS address (leave empty for AWS's own)", Optional: true, Placeholder: "https://sts.ap-south-1.amazonaws.com"},
				fNotes}},
		ConnProvider{ID: connFirmKeyRemote, Name: "Firm's own encryption key: firm's key service", Group: "Security", Test: "none",
			Purpose: "A key service the firm runs (its key in a key file or an HSM) that wraps and unwraps the firm's encryption key over HTTPS. For firms not on AWS; the protocol is in Ledgy's help",
			Fields: []ConnField{
				{Key: "url", Label: "Address", Placeholder: "https://keys.firm.in/ledgerline"},
				{Key: "secret", Label: "Shared secret (signs each request; at least 32 characters)", Secret: true, Optional: true},
				{Key: "certificate", Label: "Client certificate for mutual TLS (PEM, optional)", Optional: true},
				{Key: "private_key", Label: "Client certificate's private key (PEM, optional)", Secret: true, Optional: true},
				{Key: "server_ca", Label: "The service's CA certificate, if it is a private CA (PEM, optional)", Optional: true},
				fNotes}},
	)
	// These stay under Ledgerline's key even for a firm on its own key:
	// the credentials to reach the firm's key, and the work item that says
	// it is gone.
	vault.ServerKeyed(cConn, "firmkey-")
	vault.ServerKeyed(cWorkItem, "W-firmkey-")
}

func isFirmKeyConn(id string) bool { return id == connFirmKeyAWS || id == connFirmKeyRemote }

var (
	reAccessKey = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
	reRoleARN   = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:role/[\w+=,.@/-]{1,512}$`)
	reExtID     = regexp.MustCompile(`^[\w+=,.@:/-]{2,1000}$`)
)

// checkFirmKeyConn checks a firm-key connection before it is saved.
func checkFirmKeyConn(c *Connection) error {
	httpsOnly := func(k, what string) error {
		if x := c.Values[k]; x != "" {
			if u, err := url.Parse(x); err != nil || u.Scheme != "https" || u.Host == "" {
				return invalid("the %s must start with https://", what)
			}
		}
		return nil
	}
	switch c.Provider {
	case connFirmKeyAWS:
		if c.Values["region"] == "" {
			c.Values["region"] = vault.KMSRegions[0]
		}
		region, _, err := vault.ParseKMSKeyARN(c.Values["key_arn"])
		if err != nil {
			return invalid("%v", err)
		}
		if region != c.Values["region"] {
			return invalid("the key is in %s: choose that region", region)
		}
		if !reAccessKey.MatchString(c.Values["access_key_id"]) {
			return invalid("the access key ID is upper-case letters and digits, like AKIA…")
		}
		if r := c.Values["role_arn"]; r != "" && !reRoleARN.MatchString(r) {
			return invalid("the role must be an IAM role ARN, like arn:aws:iam::111122223333:role/LedgerlineKeyUse")
		}
		if e := c.Values["external_id"]; e != "" && !reExtID.MatchString(e) {
			return invalid("the external ID may have letters, digits and + = , . @ : / - only")
		}
		if c.Values["external_id"] != "" && c.Values["role_arn"] == "" {
			return invalid("an external ID goes with a role to assume: enter the role too")
		}
		if err := httpsOnly("sts_url", "STS address"); err != nil {
			return err
		}
	case connFirmKeyRemote:
		if err := httpsOnly("url", "address"); err != nil {
			return err
		}
		s, cert, key := c.Secrets["secret"], c.Values["certificate"], c.Secrets["private_key"]
		if s == "" && cert == "" {
			return invalid("give a shared secret, a client certificate, or both")
		}
		if s != "" && len(s) < 32 {
			return invalid("the shared secret must be at least 32 characters")
		}
		if (cert == "") != (key == "") {
			return invalid("give the client certificate and its private key together")
		}
		if cert != "" {
			if _, err := tls.X509KeyPair([]byte(cert), []byte(key)); err != nil {
				return invalid("the client certificate and private key do not load: %v", err)
			}
		}
		if ca := c.Values["server_ca"]; ca != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(ca)) {
			return invalid("the CA certificate does not load (paste it in PEM)")
		}
	}
	return nil
}

// firmKeyProvider builds the provider for a firm-key connection.
func (o *Ops) firmKeyProvider(tenant string, c Connection) (vault.KeyProvider, error) {
	switch c.Provider {
	case connFirmKeyAWS:
		return &vault.AWSKMS{Region: firstOf(c.Values["region"], vault.KMSRegions[0]), KeyARN: c.Values["key_arn"],
			Creds:   vault.AWSCredentials{AccessKeyID: c.Values["access_key_id"], SecretAccessKey: c.Secrets["secret_access_key"]},
			RoleARN: c.Values["role_arn"], ExternalID: c.Values["external_id"], SessionName: "ledgerline-" + tenant,
			Endpoint: c.Values["url"], STSEndpoint: c.Values["sts_url"], Client: o.FirmKeyClient, Now: o.Now}, nil
	case connFirmKeyRemote:
		cl, err := vault.RemoteKeyClient(c.Values["certificate"], c.Secrets["private_key"], c.Values["server_ca"], o.FirmKeyClient)
		if err != nil {
			return nil, err
		}
		return &vault.RemoteKeyService{URL: c.Values["url"], Secret: c.Secrets["secret"], Client: cl, Now: o.Now}, nil
	}
	return nil, fmt.Errorf("%s is not a firm-key connection", c.Provider)
}

// resolveFirmKey is the vault's hook: the provider for a firm on its own
// key, from the connection its key record names. It runs while the firm's
// data is locked, so it reads only that connection, which is kept under
// Ledgerline's key.
func (o *Ops) resolveFirmKey(ctx context.Context, tenant string, rec vault.FirmKeyRecord) (vault.KeyProvider, error) {
	if !isFirmKeyConn(rec.Conn) {
		return nil, &vault.ProviderError{Refused: true, Msg: "the key record names no firm-key connection"}
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, rec.Conn)
	if err != nil || c.Status == "Not set" {
		return nil, &vault.ProviderError{Refused: true, Msg: "the connection to the firm's key (Settings → Connections) is missing"}
	}
	return o.firmKeyProvider(tenant, c)
}

func firmKeyOutageKey(since time.Time) string {
	return "down-" + since.In(ist).Format("20060102T150405")
}

// firmKeyEvent is the vault's hook for a lost or returned key: an audit
// entry, and a work item for the Admin, once per outage. The server log
// gets a line too (firm code and reason; never a credential), as the
// firm's own screens cannot be shown while its key is withheld.
func (o *Ops) firmKeyEvent(tenant string, ev vault.KeyEvent) {
	ctx := context.Background()
	switch ev.Kind {
	case vault.KeyLost:
		o.log(ctx, tenant, SystemActor, "firmkey.unavailable", "", map[string]any{"reason": ev.Reason, "refused": ev.Refused, "since": ev.Since.UTC().Format(time.RFC3339)})
		slog.Warn("a firm's own encryption key is not available: its data is not served until it is", "tenant", tenant, "refused", ev.Refused, "reason", ev.Reason)
		what := "could not be reached"
		if ev.Refused {
			what = "refused Ledgerline (the key may be disabled, or Ledgerline's access to it revoked)"
		}
		o.raiseWork(ctx, tenant, firmKeyProcess, firmKeyOutageKey(ev.Since), "Your firm's encryption key is not available",
			fmt.Sprintf("Your firm's own encryption key %s at %s: %s. Until it answers, Ledgerline serves none of the firm's data and writes nothing. If this was not intended, enable the key or restore Ledgerline's access to it; service resumes by itself within a minute of the key answering.", what, ev.Since.In(ist).Format("02 Jan 2006 15:04"), ev.Reason),
			"", RoleAdmin, "settings", ev.Since.In(ist).Format("2006-01-02"))
	case vault.KeyBack:
		o.log(ctx, tenant, SystemActor, "firmkey.restored", "", map[string]any{"since": ev.Since.UTC().Format(time.RFC3339), "minutes": int(ev.At.Sub(ev.Since).Minutes())})
		slog.Info("a firm's own encryption key is available again", "tenant", tenant)
		o.closeWork(ctx, tenant, SystemActor, firmKeyProcess, firmKeyOutageKey(ev.Since), "the firm's key answered again at "+ev.At.In(ist).Format("15:04"))
	}
}

func (o *Ops) vault() *vault.Vault {
	if va := o.vaultAudit(); va != nil {
		return va.V
	}
	return nil
}

// installFirmKeys connects the vault (when the stores are encrypted) to
// the firms' key connections and to the audit trail and work queue.
func (o *Ops) installFirmKeys() {
	if v := o.vault(); v != nil {
		v.SetFirmKeyHooks(vault.FirmKeyHooks{Resolve: o.resolveFirmKey, Notify: o.firmKeyEvent, Now: func() time.Time { return o.Now() }})
	}
}

// ---- the Encryption keys panel ----

// FirmKeyRoot is one version of the firm's root key, for the screen.
type FirmKeyRoot struct {
	Version   int    `json:"version"`
	Key       string `json:"key"` // the firm's key that wraps it, masked
	WrappedAt string `json:"wrapped_at"`
	Current   bool   `json:"current"`
}

// FirmKeyConnView is a firm-key connection, for the screen.
type FirmKeyConnView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"` // Not set, Saved, Working, Problem
	Problem  string `json:"problem,omitempty"`
	Key      string `json:"key,omitempty"` // masked key ARN or the service's host
	TestedAt string `json:"tested_at,omitempty"`
}

// FirmKeyView is the Encryption keys panel.
type FirmKeyView struct {
	Mode         string            `json:"mode"`               // ledgerline or firm
	Provider     string            `json:"provider,omitempty"` // the connection's name
	Conn         string            `json:"conn,omitempty"`
	Key          string            `json:"key,omitempty"` // masked
	Region       string            `json:"region,omitempty"`
	Roots        []FirmKeyRoot     `json:"roots"`
	Current      int               `json:"current,omitempty"`
	CacheMinutes int               `json:"cache_minutes,omitempty"`
	Since        string            `json:"since,omitempty"`
	SetBy        string            `json:"set_by,omitempty"`
	RotatedAt    string            `json:"rotated_at,omitempty"`
	RotatedBy    string            `json:"rotated_by,omitempty"`
	Available    bool              `json:"available"`
	Problem      string            `json:"problem,omitempty"`
	OutageSince  string            `json:"outage_since,omitempty"`
	LastOK       string            `json:"last_ok,omitempty"`
	CachedUntil  string            `json:"cached_until,omitempty"`
	Master       string            `json:"master"` // Ledgerline's keys
	Connections  []FirmKeyConnView `json:"connections"`
	Encrypted    bool              `json:"encrypted"`
}

var reKMSARNAccount = regexp.MustCompile(`^(arn:aws:kms:[a-z0-9-]+:)([0-9]{12})(:.*)$`)

// maskKeyID shows enough of a key ARN to recognise it (region, the last
// digits of the account and of the key ID) and no more.
func maskKeyID(s string) string {
	if m := reKMSARNAccount.FindStringSubmatch(s); m != nil {
		res := m[3]
		if i := strings.IndexByte(res, '/'); i >= 0 && len(res)-i > 5 {
			res = res[:i+1] + "…" + res[len(res)-4:]
		}
		return m[1] + "••••••••" + m[2][8:] + res
	}
	return vault.MaskID(s)
}

func fmtIST(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(ist).Format(time.RFC3339)
}

func (o *Ops) firmKeyConns(ctx context.Context, tenant string) []FirmKeyConnView {
	var out []FirmKeyConnView
	for _, id := range []string{connFirmKeyAWS, connFirmKeyRemote} {
		p, _ := connProvider(id)
		cv := FirmKeyConnView{ID: id, Name: p.Name, Status: "Not set"}
		if c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id); err == nil && c.Status != "Not set" {
			cv.Status, cv.Problem, cv.TestedAt = c.Status, c.Problem, c.TestedAt
			if id == connFirmKeyAWS {
				cv.Key = maskKeyID(c.Values["key_arn"])
			} else if u, err := url.Parse(c.Values["url"]); err == nil {
				cv.Key = u.Host
			}
		}
		out = append(out, cv)
	}
	return out
}

// FirmKey is the Encryption keys panel: whose key the firm's data is under.
// Admin and Compliance (an auditor's question).
func (o *Ops) FirmKey(ctx context.Context, tenant, actor string) (FirmKeyView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the firm's encryption keys", RoleCompliance); err != nil {
		return FirmKeyView{}, err
	}
	return o.firmKeyView(ctx, tenant)
}

func (o *Ops) firmKeyView(ctx context.Context, tenant string) (FirmKeyView, error) {
	v := FirmKeyView{Mode: "ledgerline", Roots: []FirmKeyRoot{}, Connections: o.firmKeyConns(ctx, tenant)}
	vl := o.vault()
	if vl == nil {
		v.Master = "Not encrypted: the server was started without data keys."
		return v, nil
	}
	s, err := vl.FirmKey(ctx, tenant)
	if err != nil {
		return v, err
	}
	v.Encrypted, v.Master = true, s.MasterSource
	if !s.Own {
		v.Problem = s.Problem
		return v, nil
	}
	v.Mode, v.Conn, v.Current, v.CacheMinutes = "firm", s.Conn, s.Current, s.CacheMinutes
	if p, ok := connProvider(s.Conn); ok {
		v.Provider = p.Name
	}
	for _, r := range s.Roots {
		v.Roots = append(v.Roots, FirmKeyRoot{Version: r.Version, Key: maskKeyID(r.KeyID), WrappedAt: fmtIST(r.WrappedAt), Current: r.Version == s.Current})
		if r.Version == s.Current {
			v.Key = maskKeyID(r.KeyID)
			if reg, _, err := vault.ParseKMSKeyARN(r.KeyID); err == nil {
				v.Region = reg
			}
		}
	}
	v.Since, v.SetBy, v.RotatedAt, v.RotatedBy = fmtIST(s.Since), s.SetBy, fmtIST(s.RotatedAt), s.RotatedBy
	v.Available, v.Problem, v.LastOK, v.CachedUntil = s.Available, s.Problem, fmtIST(s.LastOK), fmtIST(s.CachedUntil)
	if s.Outage != nil {
		v.OutageSince = fmtIST(s.Outage.Since)
		v.Problem = firstOf(v.Problem, s.Outage.Reason)
	}
	return v, nil
}

func (o *Ops) firmKeyConn(ctx context.Context, tenant, id string) (Connection, vault.KeyProvider, error) {
	if !isFirmKeyConn(id) {
		return Connection{}, nil, invalid("choose AWS KMS or the firm's key service")
	}
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, id)
	if errors.Is(err, docs.ErrNotFound) || (err == nil && c.Status == "Not set") {
		p, _ := connProvider(id)
		return Connection{}, nil, invalid("enter the key's details first in Settings → Connections → %s", p.Name)
	}
	if err != nil {
		return Connection{}, nil, err
	}
	prov, err := o.firmKeyProvider(tenant, c)
	if err != nil {
		return Connection{}, nil, invalid("%v", err)
	}
	return c, prov, nil
}

// firmKeyConnTest is the connection's test: a throwaway key wrapped and
// unwrapped with the firm's key, with the firm's encryption context. Nothing
// is stored. When the firm is on this key, its stored keys are unwrapped
// afresh too.
func (o *Ops) firmKeyConnTest(ctx context.Context, tenant, id string) (string, string) {
	_, prov, err := o.firmKeyConn(ctx, tenant, id)
	if err == nil {
		_, err = vault.TestFirmKey(ctx, prov, tenant)
	}
	if err == nil {
		if vl := o.vault(); vl != nil {
			if s, _ := vl.FirmKey(ctx, tenant); s.Own && s.Conn == id {
				_, err = vl.CheckFirmKey(ctx, tenant)
			}
		}
	}
	if err != nil {
		return "Problem", strings.TrimPrefix(err.Error(), "invalid: ")
	}
	return "Working", "wrapped and unwrapped a test key with the firm's encryption context"
}

// TestFirmKey tests a firm-key connection from the panel (as Test on the
// Connections page does).
func (o *Ops) TestFirmKey(ctx context.Context, tenant, actor, id string) (FirmKeyView, error) {
	if !isFirmKeyConn(id) {
		return FirmKeyView{}, invalid("choose AWS KMS or the firm's key service")
	}
	if _, err := o.TestConnection(ctx, tenant, actor, id); err != nil {
		return FirmKeyView{}, err
	}
	return o.firmKeyView(ctx, tenant)
}

// UseFirmKey moves the firm onto its own key. The key wraps and unwraps
// the firm's new root key first; nothing is written under a key that does
// not do both. Admin only.
func (o *Ops) UseFirmKey(ctx context.Context, tenant, actor, id string, cacheMinutes int) (FirmKeyView, error) {
	if err := o.require(ctx, tenant, actor, "Moving the firm onto its own encryption key", RoleAdmin); err != nil {
		return FirmKeyView{}, err
	}
	vl := o.vault()
	if vl == nil {
		return FirmKeyView{}, fmt.Errorf("%w: this server does not encrypt data", ErrState)
	}
	if cacheMinutes != 0 && (cacheMinutes < vault.MinCacheMinutes || cacheMinutes > vault.MaxCacheMinutes) {
		return FirmKeyView{}, invalid("keep the firm's keys in memory for %d to %d minutes", vault.MinCacheMinutes, vault.MaxCacheMinutes)
	}
	c, prov, err := o.firmKeyConn(ctx, tenant, id)
	if err != nil {
		return FirmKeyView{}, err
	}
	s, err := vl.UseFirmKey(ctx, tenant, id, prov, cacheMinutes, actor)
	if err != nil {
		o.log(ctx, tenant, actor, "firmkey.move_failed", id, map[string]any{"problem": err.Error()})
		return FirmKeyView{}, fmt.Errorf("%w: %v", ErrState, err)
	}
	key := ""
	if len(s.Roots) > 0 {
		key = maskKeyID(s.Roots[0].KeyID)
	}
	o.log(ctx, tenant, actor, "firmkey.moved", id, map[string]any{"key": key, "root_version": s.Current, "cache_minutes": s.CacheMinutes, "region": c.Values["region"]})
	return o.firmKeyView(ctx, tenant)
}

// RotateFirmKey makes a new root version for new writes and re-wraps the
// older ones under the key the firm's connection names now. Admin only.
func (o *Ops) RotateFirmKey(ctx context.Context, tenant, actor string) (FirmKeyView, error) {
	if err := o.require(ctx, tenant, actor, "Rotating the firm's encryption key", RoleAdmin); err != nil {
		return FirmKeyView{}, err
	}
	vl := o.vault()
	if vl == nil {
		return FirmKeyView{}, fmt.Errorf("%w: this server does not encrypt data", ErrState)
	}
	s, err := vl.FirmKey(ctx, tenant)
	if err != nil {
		return FirmKeyView{}, err
	}
	if !s.Own {
		return FirmKeyView{}, fmt.Errorf("%w: the firm's data is under Ledgerline's key; set up the firm's own key first", ErrState)
	}
	_, prov, err := o.firmKeyConn(ctx, tenant, s.Conn)
	if err != nil {
		return FirmKeyView{}, err
	}
	ns, err := vl.RotateFirmKey(ctx, tenant, prov, actor)
	if err != nil {
		o.log(ctx, tenant, actor, "firmkey.rotate_failed", s.Conn, map[string]any{"problem": err.Error()})
		return FirmKeyView{}, fmt.Errorf("%w: %v", ErrState, err)
	}
	key := ""
	if len(ns.Roots) > 0 {
		key = maskKeyID(ns.Roots[0].KeyID)
	}
	o.log(ctx, tenant, actor, "firmkey.rotated", s.Conn, map[string]any{"key": key, "root_version": ns.Current, "rewrapped": len(ns.Roots) - 1})
	return o.firmKeyView(ctx, tenant)
}

// SetFirmKeyCache sets how long the firm's unwrapped keys stay in memory,
// which is how soon a revocation takes effect. Admin only.
func (o *Ops) SetFirmKeyCache(ctx context.Context, tenant, actor string, minutes int) (FirmKeyView, error) {
	if err := o.require(ctx, tenant, actor, "Changing how long the firm's keys stay in memory", RoleAdmin); err != nil {
		return FirmKeyView{}, err
	}
	vl := o.vault()
	if vl == nil {
		return FirmKeyView{}, fmt.Errorf("%w: this server does not encrypt data", ErrState)
	}
	if minutes < vault.MinCacheMinutes || minutes > vault.MaxCacheMinutes {
		return FirmKeyView{}, invalid("keep the firm's keys in memory for %d to %d minutes", vault.MinCacheMinutes, vault.MaxCacheMinutes)
	}
	if _, err := vl.SetFirmKeyCache(ctx, tenant, minutes, actor); err != nil {
		return FirmKeyView{}, fmt.Errorf("%w: %v", ErrState, err)
	}
	o.log(ctx, tenant, actor, "firmkey.cache_set", "", map[string]any{"minutes": minutes})
	return o.firmKeyView(ctx, tenant)
}

// checkFirmKeyInUse refuses a change to the connection the firm's data is
// under that would lock the firm out: the new details must still unwrap
// the firm's keys. Removing it is refused outright.
func (o *Ops) checkFirmKeyInUse(ctx context.Context, tenant string, nc *Connection, removing bool) error {
	vl := o.vault()
	if vl == nil {
		return nil
	}
	s, err := vl.FirmKey(ctx, tenant)
	if err != nil || !s.Own || s.Conn != nc.Provider {
		return nil
	}
	if removing {
		return fmt.Errorf("%w: the firm's data is under this key; it cannot be removed", ErrState)
	}
	prov, err := o.firmKeyProvider(tenant, *nc)
	if err != nil {
		return invalid("%v", err)
	}
	if err := vl.VerifyFirmKeyProvider(ctx, tenant, prov); err != nil {
		return invalid("with these details the firm's keys do not unwrap, so the change was not saved (%v). To move to a new KMS key, keep the old key working, enter the new key's ARN and then rotate", err)
	}
	return nil
}
