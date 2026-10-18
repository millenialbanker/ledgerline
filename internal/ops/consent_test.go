package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

func TestWhatsAppConsent(t *testing.T) {
	o, ctx := setup(t)
	c, _ := o.Client(ctx, T, "BH1001")
	c.WhatsApp, c.WAConsent = false, nil
	_ = docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test")

	// Writing to the bot does not opt in; YES does, STOP undoes it.
	reply, _ := o.WhatsAppInbound(ctx, T, c.Mobile, "HELP", D)
	if c, _ = o.Client(ctx, T, "BH1001"); c.WhatsApp || !strings.Contains(reply, "Reply YES") {
		t.Fatalf("opted in by BAL: %v %q", c.WhatsApp, reply)
	}
	_, _ = o.WhatsAppInbound(ctx, T, c.Mobile, "YES", D)
	if c, _ = o.Client(ctx, T, "BH1001"); !c.WhatsApp || c.WAConsent == nil || c.WAConsent.Source != SrcWhatsApp {
		t.Fatalf("YES: %+v", c.WAConsent)
	}
	_, _ = o.WhatsAppInbound(ctx, T, c.Mobile, "STOP", D)
	if c, _ = o.Client(ctx, T, "BH1001"); c.WhatsApp || c.WAConsent.On || len(c.WAConsent.History) != 2 {
		t.Fatalf("STOP: %+v", c.WAConsent)
	}

	// Staff record a call only with a reference.
	if _, err := o.RecordConsent(ctx, T, "priya.nair", "BH1002", true, SrcCall, ""); err == nil {
		t.Fatal("consent without a reference")
	}
	if _, err := o.RecordConsent(ctx, T, "anita.rao", "BH1002", true, SrcCall, "REC-1"); err == nil {
		t.Fatal("risk manager recorded consent")
	}
	if c2, err := o.RecordConsent(ctx, T, "priya.nair", "BH1002", true, SrcCall, "REC-1"); err != nil || !c2.WhatsApp || c2.WAConsent.By != "priya.nair" {
		t.Fatalf("call: %+v %v", c2.WAConsent, err)
	}

	// Campaign: a link to everyone not asked; answering yes records it.
	if _, err := o.ConsentCampaign(ctx, T, "priya.nair", ChEmail, "https://bo.example", 0); err == nil {
		t.Fatal("maker ran a campaign")
	}
	before, _ := o.Consents(ctx, T, nil)
	res, err := o.ConsentCampaign(ctx, T, "farhan.khan", ChEmail, "https://bo.example", 0)
	if err != nil || res.Invited == 0 {
		t.Fatalf("campaign: %+v %v", res, err)
	}
	if res.Invited+res.Skipped != before.Counts["Not asked"] {
		t.Fatalf("asked %d+%d of %d not asked", res.Invited, res.Skipped, before.Counts["Not asked"])
	}
	invs, _ := docs.List[WAInvite](ctx, o.Docs, T, cWAInvite, "")
	var inv WAInvite
	for _, x := range invs {
		if x.UCC == "BH1003" {
			inv = x
		}
	}
	ms, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	found := false
	for _, m := range ms {
		found = found || (m.UCC == "BH1003" && strings.Contains(m.Body, "https://bo.example/optin/"+T+"/"+inv.Token))
	}
	if inv.Token == "" || !found {
		t.Fatal("no consent link sent to BH1003")
	}
	p, _, err := o.OpenConsent(ctx, T, inv.Token)
	if err != nil || p.Answer != "" || p.Name == "" {
		t.Fatalf("open: %+v %v", p, err)
	}
	if p, err = o.AnswerConsent(ctx, T, inv.Token, true); err != nil || p.Answer != "Yes" {
		t.Fatalf("answer: %+v %v", p, err)
	}
	if c3, _ := o.Client(ctx, T, "BH1003"); !c3.WhatsApp || c3.WAConsent.Source != SrcLink {
		t.Fatalf("link consent: %+v", c3.WAConsent)
	}
	// A second answer changes nothing; a second campaign asks no one again.
	_, _ = o.AnswerConsent(ctx, T, inv.Token, false)
	if c3, _ := o.Client(ctx, T, "BH1003"); !c3.WhatsApp {
		t.Fatal("answer changed twice")
	}
	if res2, _ := o.ConsentCampaign(ctx, T, "farhan.khan", ChEmail, "https://bo.example", 0); res2.Invited != 0 {
		t.Fatalf("asked again: %+v", res2)
	}
	if _, _, err := o.OpenConsent(ctx, T, "nope"); err == nil {
		t.Fatal("bad token opened")
	}
	v, _ := o.Consents(ctx, T, nil)
	t.Logf("counts %v", v.Counts)
}

func TestOnboardingConsent(t *testing.T) {
	o, ctx := setup(t)
	c, _ := o.Client(ctx, T, "BH1001")
	c.UCC, c.PAN, c.Name, c.WhatsApp, c.WAConsent = "", "ABCPZ9999K", "Kavya Iyer", true, &WAConsent{On: true, Source: "forged"}
	n, err := o.CreateApplication(ctx, T, "priya.nair", c)
	if err != nil {
		t.Fatal(err)
	}
	if n.WAConsent == nil || n.WAConsent.Source != SrcOnboarding || n.WAConsent.By != "priya.nair" {
		t.Fatalf("onboarding consent: %+v", n.WAConsent)
	}
}
