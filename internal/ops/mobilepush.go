package ops

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- push notifications (Firebase Cloud Messaging HTTP v1) ----
//
// One connection serves both apps on both platforms: FCM delivers to
// Android itself and to iPhones through Apple's push service (APNs), once
// the firm's Firebase project holds its APNs key.
//
// The rule: a push never carries client data. Its words come from the fixed
// templates below ("3 approvals waiting", "Your contract note is ready"),
// with at most a count; no client code, name, PAN, amount or scrip. The
// details are fetched inside the app after the phone is unlocked. Because
// nothing about a client goes to Google or Apple, push is allowed in
// India-only mode too.
//
// Pushes wait in an outbox (pushout) and the scheduler's tick sends them,
// so a burst of events becomes one push per person and kind ("5 approvals
// waiting", not five pushes).

const (
	connFCM  = "fcm"
	cPushOut = "pushout"

	fcmScope   = "https://www.googleapis.com/auth/firebase.messaging"
	fcmAPIBase = "https://fcm.googleapis.com"
)

// The kinds of push. Each has fixed words; PushToStaff and the hooks below
// take only these.
const (
	PushApprovals   = "approvals" // staff: prepared actions waiting for the person
	PushWork        = "work"      // staff: new items in the person's work queue
	PushAlert       = "alert"     // staff: a surveillance alert was raised
	PushStepOverdue = "overdue"   // staff: a step of the day is overdue
	PushDocument    = "document"  // client: a contract note or statement is ready
	PushNote        = "note"      // client: a contract note is ready
	PushMoney       = "money"     // client: payouts and money received
	PushRequest     = "request"   // client: a profile, IPO, pledge or demat request changed
	PushHelp        = "help"      // client: the help desk replied
	PushAccount     = "account"   // client: something to look at in the account
	PushTest        = "test"      // a test from Settings
)

type pushText struct {
	app, route string
	words      func(n int) (title, body string)
}

func countWords(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

var pushTexts = map[string]pushText{
	PushApprovals: {AppStaff, "approvals", func(n int) (string, string) {
		return "Approvals waiting", countWords(n, "approval is", "approvals are") + " waiting for you. Open the app to see them."
	}},
	PushWork: {AppStaff, "work", func(n int) (string, string) {
		return "New work", countWords(n, "new item is", "new items are") + " in your work queue."
	}},
	PushAlert: {AppStaff, "alerts", func(n int) (string, string) {
		return "Alert raised", countWords(n, "new alert needs", "new alerts need") + " review."
	}},
	PushStepOverdue: {AppStaff, "today", func(n int) (string, string) {
		return "Step overdue", countWords(n, "step of today is", "steps of today are") + " overdue."
	}},
	PushNote:     {AppClient, "documents", func(int) (string, string) { return "Contract note", "Your contract note is ready." }},
	PushDocument: {AppClient, "documents", func(int) (string, string) { return "New statement", "A new statement is ready in the app." }},
	PushMoney: {AppClient, "funds", func(int) (string, string) {
		return "Funds", "There is an update on your funds. Open the app to see it."
	}},
	PushRequest: {AppClient, "requests", func(int) (string, string) { return "Your request", "There is an update on one of your requests." }},
	PushHelp:    {AppClient, "help", func(int) (string, string) { return "Help desk", "We have replied to your request." }},
	PushAccount: {AppClient, "home", func(int) (string, string) { return "Your account", "There is something new in your account." }},
	PushTest:    {"", "home", func(int) (string, string) { return "Test", "Push notifications work on this phone." }},
}

// clientPushFor maps a client message event (messaging.go) to a push kind;
// "" sends no push.
func clientPushFor(event string) string {
	switch {
	case event == "contract_note.sent":
		return PushNote
	case strings.HasPrefix(event, "statement."):
		return PushDocument
	case strings.HasPrefix(event, "payout.") || strings.HasPrefix(event, "receipt.") || strings.HasPrefix(event, "debit.") || strings.HasPrefix(event, "margin."):
		return PushMoney
	case strings.HasPrefix(event, "ticket."):
		return PushHelp
	case strings.HasPrefix(event, "ipo.") || strings.HasPrefix(event, "edis.") || strings.HasPrefix(event, "cdsl.") || strings.HasPrefix(event, "change.") || strings.HasPrefix(event, "dp."):
		return PushRequest
	case event == "portal.granted" || strings.HasPrefix(event, "legacy_debt."):
		return ""
	}
	return PushAccount
}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connFCM, Name: "Push notifications (Firebase Cloud Messaging)", Group: "Messages", Test: "none",
		Purpose: "Push notifications to the staff app and the client app, on Android and iPhone (FCM HTTP v1 sends to iPhones through Apple's push service). " +
			"A push never carries client data: only fixed words such as \"3 approvals waiting\" or \"Your contract note is ready\"; the details open in the app after the phone is unlocked. " +
			"That is why push is allowed in India-only mode: nothing about a client leaves India. Paste the Firebase service account key (a JSON file from Firebase → Project settings → Service accounts).",
		Fields: []ConnField{
			{Key: "service_account", Label: "Service account key (the JSON file's contents)", Secret: true},
			{Key: "project_id", Label: "Firebase project ID (blank: the one in the key)", Optional: true},
			fNotes,
		}})
}

// fcmConf is a working FCM connection.
type fcmConf struct {
	Project, Email, KeyID, TokenURI string
	Key                             *rsa.PrivateKey
}

type serviceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	KeyID       string `json:"private_key_id"`
	PrivateKey  string `json:"private_key"`
	ClientEmail string `json:"client_email"`
	TokenURI    string `json:"token_uri"`
}

func parseFCM(c Connection) (fcmConf, error) {
	var sa serviceAccount
	if err := json.Unmarshal([]byte(c.Secrets["service_account"]), &sa); err != nil {
		return fcmConf{}, invalid("the service account key must be the JSON file from Firebase")
	}
	if sa.Type != "service_account" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return fcmConf{}, invalid("this JSON is not a service account key (type service_account, with client_email and private_key)")
	}
	b, _ := pem.Decode([]byte(sa.PrivateKey))
	if b == nil {
		return fcmConf{}, invalid("the key's private_key is not a PEM key")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		if k1, err1 := x509.ParsePKCS1PrivateKey(b.Bytes); err1 == nil {
			k, err = k1, nil
		}
	}
	rk, ok := k.(*rsa.PrivateKey)
	if err != nil || !ok {
		return fcmConf{}, invalid("the key's private_key must be an RSA key")
	}
	tu := firstOf(sa.TokenURI, "https://oauth2.googleapis.com/token")
	if u, err := url.Parse(tu); err != nil || u.Scheme != "https" || u.Host == "" {
		return fcmConf{}, invalid("the key's token_uri must be an https:// address")
	}
	p := firstOf(strings.TrimSpace(c.Values["project_id"]), sa.ProjectID)
	if p == "" {
		return fcmConf{}, invalid("enter the Firebase project ID")
	}
	return fcmConf{Project: p, Email: sa.ClientEmail, KeyID: sa.KeyID, TokenURI: tu, Key: rk}, nil
}

func checkFCMConn(c Connection) error {
	_, err := parseFCM(c)
	return err
}

func (o *Ops) fcmConn(ctx context.Context, tenant string) (fcmConf, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connFCM)
	if err != nil || c.Secrets["service_account"] == "" {
		return fcmConf{}, false
	}
	f, err := parseFCM(c)
	return f, err == nil
}

// ---- the outbox ----

// PushMsg is one push waiting for, or sent to, one person's phones. It
// holds the template kind and a count, never client data.
type PushMsg struct {
	ID      string `json:"id"`
	User    string `json:"user"`
	App     string `json:"app"`
	Kind    string `json:"kind"`
	Count   int    `json:"count"`
	Status  string `json:"status"` // Pending, Sent, Not sent
	Queued  string `json:"queued"`
	SentAt  string `json:"sent_at,omitempty"`
	Devices int    `json:"devices,omitempty"`
	Note    string `json:"note,omitempty"`
	Tries   int    `json:"tries,omitempty"`
}

// PushToStaff queues a push of a staff kind to staff users who use the
// staff app. Other features call it; for example the step reminders:
//
//	o.PushToStaff(ctx, tenant, []string{"farhan.khan"}, ops.PushStepOverdue)
//
// It sends nothing that a template does not say, so the caller cannot put
// client data in a push.
func (o *Ops) PushToStaff(ctx context.Context, tenant string, users []string, kind string) error {
	t, ok := pushTexts[kind]
	if !ok || t.app != AppStaff {
		return invalid("unknown staff push %q", kind)
	}
	if !o.mobileSettings(ctx, tenant).StaffOn {
		return nil
	}
	for _, u := range users {
		if o.StaffAppAccess(ctx, tenant, u) == nil {
			o.queuePush(ctx, tenant, u, AppStaff, kind, 1)
		}
	}
	return nil
}

// queuePush adds to the person's pending push of this kind, or starts one,
// when the person has a phone that takes pushes for the app.
func (o *Ops) queuePush(ctx context.Context, tenant, user, app, kind string, n int) {
	ds, err := o.devicesOf(ctx, tenant, user)
	if err != nil {
		return
	}
	has := false
	for _, d := range ds {
		has = has || (d.PushToken != "" && (d.App == app || app == ""))
	}
	if !has {
		return
	}
	id := "P-" + shortHash(user, app, kind)
	now := o.now().Format(time.RFC3339)
	upd(ctx, o.Docs, tenant, cPushOut, id, SystemActor, &PushMsg{}, func(m *PushMsg) error {
		if m.Status != "Pending" {
			*m = PushMsg{ID: id, User: user, App: app, Kind: kind, Status: "Pending", Queued: now}
		}
		m.Count += n
		return nil
	})
}

// pushNewWork tells the people who should act on a new work item. A
// prepared action is an approval; anything else is work in their queue.
func (o *Ops) pushNewWork(ctx context.Context, tenant string, w WorkItem) {
	m := o.mobileSettings(ctx, tenant)
	if !m.StaffOn || w.Role == "" {
		return
	}
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return
	}
	branch := ""
	if w.Role == RoleBranch && w.UCC != "" {
		if c, err := o.Client(ctx, tenant, w.UCC); err == nil {
			branch = c.Branch
		}
	}
	kind := PushWork
	if w.Proposed != nil {
		kind = PushApprovals
	}
	scope := map[string]func(string) bool{}
	for _, u := range us {
		if !u.Active || u.Role != w.Role || (branch != "" && u.Branch != branch) || o.StaffAppAccess(ctx, tenant, u.ID) != nil {
			continue
		}
		if w.UCC != "" && (u.Role == RoleBranch || u.Role == RoleAP) {
			f, ok := scope[u.ID]
			if !ok {
				f = o.ClientScope(ctx, tenant, u.ID)
				scope[u.ID] = f
			}
			if f != nil && !f(w.UCC) {
				continue
			}
		}
		o.queuePush(ctx, tenant, u.ID, AppStaff, kind, 1)
	}
}

// pushNewAlert tells the compliance officers who use the staff app.
func (o *Ops) pushNewAlert(ctx context.Context, tenant string) {
	if !o.mobileSettings(ctx, tenant).StaffOn {
		return
	}
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return
	}
	for _, u := range us {
		if u.Active && u.Role == RoleCompliance && o.StaffAppAccess(ctx, tenant, u.ID) == nil {
			o.queuePush(ctx, tenant, u.ID, AppStaff, PushAlert, 1)
		}
	}
}

// pushClientEvent sends the client app's push for a client message event
// to every client login that covers the account (the client's own and
// family logins).
func (o *Ops) pushClientEvent(ctx context.Context, tenant, event, ucc string) {
	kind := clientPushFor(event)
	if kind == "" || ucc == "" || !o.mobileSettings(ctx, tenant).Client.On {
		return
	}
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return
	}
	for _, u := range us {
		if u.Active && u.Role == RoleClient && contains(u.Clients, strings.ToUpper(ucc)) {
			o.queuePush(ctx, tenant, u.ID, AppClient, kind, 1)
		}
	}
}

// ---- sending ----

// fcmMessage is the FCM HTTP v1 request (projects.messages.send): a
// notification for the system tray, and data the app reads when tapped.
type fcmMessage struct {
	Message struct {
		Token        string            `json:"token"`
		Notification map[string]string `json:"notification"`
		Data         map[string]string `json:"data"`
		Android      map[string]any    `json:"android"`
		APNS         map[string]any    `json:"apns"`
	} `json:"message"`
}

// pushPayload builds the request for one phone. Its words come only from
// the template and the count.
func pushPayload(token string, m PushMsg, n int) fcmMessage {
	t := pushTexts[m.Kind]
	title, body := t.words(n)
	var f fcmMessage
	f.Message.Token = token
	f.Message.Notification = map[string]string{"title": title, "body": body}
	f.Message.Data = map[string]string{"kind": m.Kind, "route": t.route, "app": m.App, "count": strconv.Itoa(n)}
	f.Message.Android = map[string]any{"priority": "HIGH", "collapse_key": m.Kind, "notification": map[string]any{"tag": m.Kind}}
	f.Message.APNS = map[string]any{"headers": map[string]string{"apns-priority": "10", "apns-collapse-id": m.Kind},
		"payload": map[string]any{"aps": map[string]any{"sound": "default", "thread-id": m.Kind}}}
	return f
}

func (o *Ops) fcmHTTP() *http.Client {
	if o.FCMClient != nil {
		return o.FCMClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// fcmToken gets (or reuses) an OAuth access token: a JWT signed with the
// service account's key, exchanged at its token_uri (RFC 7523).
func (o *Ops) fcmToken(ctx context.Context, tenant string, c fcmConf, fresh bool) (string, error) {
	type cached struct {
		tok, who string
		until    time.Time
	}
	now := o.Now()
	if v, ok := o.fcmTok.Load(tenant); ok && !fresh {
		if x := v.(cached); x.who == c.Email+c.KeyID && now.Before(x.until) {
			return x.tok, nil
		}
	}
	enc := base64.RawURLEncoding
	hd, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": c.KeyID})
	cl, _ := json.Marshal(map[string]any{"iss": c.Email, "scope": fcmScope, "aud": c.TokenURI, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	in := enc.EncodeToString(hd) + "." + enc.EncodeToString(cl)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(nil, c.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {in + "." + enc.EncodeToString(sig)}}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.TokenURI, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := o.fcmHTTP().Do(req)
	if err != nil {
		return "", fmt.Errorf("Google sign-in: %w", err)
	}
	defer res.Body.Close()
	var t struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&t)
	if res.StatusCode != 200 || t.Token == "" {
		return "", fmt.Errorf("Google refused the service account key (HTTP %d %s %s)", res.StatusCode, t.Error, t.Desc)
	}
	life := time.Duration(max(t.Expires, 120)) * time.Second
	o.fcmTok.Store(tenant, cached{tok: t.Token, who: c.Email + c.KeyID, until: now.Add(life - time.Minute)})
	return t.Token, nil
}

// errBadToken is FCM saying the phone's token no longer exists.
var errBadToken = errors.New("the phone's push token is no longer valid")

// errPushRetry is a failure worth trying again on the next tick.
var errPushRetry = errors.New("FCM busy; trying again")

func (o *Ops) fcmSend(ctx context.Context, tenant string, c fcmConf, msg fcmMessage) error {
	body, _ := json.Marshal(msg)
	base := strings.TrimRight(firstOf(o.FCMBase, fcmAPIBase), "/")
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := o.fcmToken(ctx, tenant, c, attempt > 0)
		if err != nil {
			return err
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/v1/projects/"+url.PathEscape(c.Project)+"/messages:send", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := o.fcmHTTP().Do(req)
		if err != nil {
			return fmt.Errorf("%w: %v", errPushRetry, err)
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		if res.StatusCode == 200 {
			return nil
		}
		var e struct {
			Error struct {
				Status  string `json:"status"`
				Message string `json:"message"`
				Details []struct {
					Code string `json:"errorCode"`
				} `json:"details"`
			} `json:"error"`
		}
		json.Unmarshal(b, &e)
		code := ""
		for _, d := range e.Error.Details {
			code = firstOf(code, d.Code)
		}
		switch {
		case res.StatusCode == 401 && attempt == 0:
			continue // the access token expired early: get a new one
		case res.StatusCode == 404 || code == "UNREGISTERED" || (res.StatusCode == 400 && code == "INVALID_ARGUMENT" && strings.Contains(strings.ToLower(e.Error.Message), "token")):
			return errBadToken
		case res.StatusCode == 429 || res.StatusCode >= 500:
			return errPushRetry
		}
		return fmt.Errorf("FCM refused the push (HTTP %d %s %s)", res.StatusCode, firstOf(code, e.Error.Status), e.Error.Message)
	}
	return errors.New("FCM refused the access token")
}

// SendPushes sends the pending pushes. The tick calls it; tests and the
// test button call it directly.
func (o *Ops) SendPushes(ctx context.Context, tenant string) (int, error) {
	ms, err := docs.List[PushMsg](ctx, o.Docs, tenant, cPushOut, "")
	if err != nil {
		return 0, err
	}
	var pending []PushMsg
	for _, m := range ms {
		if m.Status == "Pending" {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Queued < pending[j].Queued })
	c, ok := o.fcmConn(ctx, tenant)
	sent := 0
	for _, m := range pending {
		m.Tries++
		note, n := "", m.Count
		if m.Kind == PushApprovals {
			// The count is what is waiting now, not how many came.
			if k := o.staffWaitingCount(ctx, tenant, m.User); k > 0 {
				n = k
			}
		}
		devs := 0
		if !ok {
			note = "push notifications are not connected (Settings → Connections)"
		} else {
			ds, _ := o.devicesOf(ctx, tenant, m.User)
			retry := false
			for _, d := range ds {
				if d.PushToken == "" || (m.App != "" && d.App != m.App) {
					continue
				}
				err := o.fcmSend(ctx, tenant, c, pushPayload(d.PushToken, m, n))
				switch {
				case err == nil:
					devs++
				case errors.Is(err, errBadToken):
					// The app was removed or the token replaced: forget it;
					// the app sends a new one when it next opens.
					docs.Update(ctx, o.Docs, tenant, cMobileDev, d.ID, SystemActor, func(x *MobileDevice) error {
						if x.PushToken == d.PushToken {
							x.PushToken = ""
						}
						return nil
					})
					note = "a phone's push token had expired"
				case errors.Is(err, errPushRetry) && m.Tries < 5:
					retry = true
				default:
					note = err.Error()
				}
			}
			if retry && devs == 0 {
				docs.Upsert(ctx, o.Docs, tenant, cPushOut, m.ID, m, SystemActor)
				continue
			}
		}
		m.Status, m.SentAt, m.Devices, m.Note = "Sent", o.now().Format(time.RFC3339), devs, note
		if devs == 0 {
			m.Status = "Not sent"
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cPushOut, m.ID, m, SystemActor); err != nil {
			return sent, err
		}
		if devs > 0 {
			sent++
		}
		o.log(ctx, tenant, SystemActor, "mobile.push", m.ID, map[string]any{"user": m.User, "kind": m.Kind, "count": n, "phones": devs, "note": note})
	}
	return sent, nil
}

// pushTick sends what is waiting; the scheduler calls it every minute.
func (o *Ops) pushTick(ctx context.Context, tenant string) {
	o.SendPushes(ctx, tenant)
}

// TestPush sends a test push to the person's own phones at once.
func (o *Ops) TestPush(ctx context.Context, tenant, actor string) (int, error) {
	if _, ok := o.fcmConn(ctx, tenant); !ok {
		return 0, invalid("connect push notifications first (Settings → Connections → Push notifications)")
	}
	ds, err := o.devicesOf(ctx, tenant, actor)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range ds {
		if d.PushToken != "" {
			n++
		}
	}
	if n == 0 {
		return 0, invalid("none of your phones takes pushes yet: open the app on your phone, sign in and allow notifications")
	}
	o.queuePush(ctx, tenant, actor, "", PushTest, 1)
	return o.SendPushes(ctx, tenant)
}

// MyPushes are the pushes sent to the person, newest first: the app's
// notifications list (words only, as the push said them).
type MyPush struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Route string `json:"route"`
	At    string `json:"at"`
}

// Notifications lists the pushes sent to a person (one per kind: the
// latest).
func (o *Ops) Notifications(ctx context.Context, tenant, actor string) ([]MyPush, error) {
	ms, err := docs.List[PushMsg](ctx, o.Docs, tenant, cPushOut, "")
	if err != nil {
		return nil, err
	}
	out := []MyPush{}
	for _, m := range ms {
		if m.User != actor || m.Status == "Pending" {
			continue
		}
		t, ok := pushTexts[m.Kind]
		if !ok {
			continue
		}
		title, body := t.words(max(m.Count, 1))
		out = append(out, MyPush{Kind: m.Kind, Title: title, Body: body, Route: t.route, At: firstOf(m.SentAt, m.Queued)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, nil
}
