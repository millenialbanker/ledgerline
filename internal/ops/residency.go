package ops

import (
	"regexp"
)

// ---- data residency: where client data goes ----
//
// SEBI's cloud framework expects regulated data, logs and encryption keys
// to stay in India. Ledgerline keeps everything in its own database and
// serves every page asset itself; the only ways data can leave are the
// providers it is connected to. This lists each one and where it is, and
// India-only mode (LEDGERLINE_INDIA_ONLY=1) refuses to start a server with
// a connection known to be outside India.

// Residency is the server's declared hosting.
type Residency struct {
	IndiaOnly bool
	Region    string // as declared, e.g. "AWS Mumbai (ap-south-1)"
	Database  string // database host
}

// ResidencyLine is one place data is kept or sent.
type ResidencyLine struct {
	What   string `json:"what"`
	Where  string `json:"where"`
	Status string `json:"status"` // india, outside, none, check
	Note   string `json:"note,omitempty"`
}

var reIndiaRegion = regexp.MustCompile(`(?i)\b(india|mumbai|hyderabad|chennai|delhi|pune|bengaluru|bangalore|noida|ap-south-[12]|centralindia|southindia|westindia|asia-south[12])\b`)

// InIndia reports whether a declared region names a place in India.
func InIndia(region string) bool { return reIndiaRegion.MatchString(region) }

// ResidencyLines lists where client data is kept and sent.
func (o *Ops) ResidencyLines() []ResidencyLine {
	r := o.Residency
	host := ResidencyLine{What: "Server, database, stored files and backups", Where: r.Region, Status: "check"}
	switch {
	case r.Region == "":
		host.Where, host.Note = "Not declared", "Set LEDGERLINE_REGION to the hosting region (for example AWS Mumbai, ap-south-1) and keep backups and the standby copy in India too."
	case InIndia(r.Region):
		host.Status = "india"
	default:
		host.Status, host.Note = "outside", "The declared region is not in India."
	}
	if r.Database != "" {
		host.Note = trimNote(host.Note + " Database: " + r.Database + ".")
	}
	keys := ResidencyLine{What: "Encryption keys", Where: "This server (LEDGERLINE_DATA_KEYS)", Status: host.Status}
	if host.Status == "check" {
		keys.Note = "Keys are wherever the server is. With a key service, use one in an Indian region."
	}
	lines := []ResidencyLine{host, keys,
		{What: "Pages, scripts and fonts", Where: "Served by Ledgerline", Status: "india", Note: "Browsers contact no other site."},
		{What: "Ledgy help", Where: "Help articles on this server", Status: "india"},
	}
	if o.LedgyAI != nil {
		where := map[string]string{"anthropic": "Anthropic (USA)", "openai": "OpenAI (USA)", "gemini": "Google Gemini API (outside India)"}[o.LedgyAI.Provider()]
		lines = append(lines, ResidencyLine{What: "Ledgy AI phrasing", Where: where, Status: "outside", Note: "Only when a firm switches it on: the masked question and help articles, never client data. Not allowed in India-only mode."})
	} else {
		lines = append(lines, ResidencyLine{What: "Ledgy AI phrasing", Where: "Not connected", Status: "none"})
	}
	if o.KYC != nil {
		lines = append(lines, ResidencyLine{What: "Online account opening checks", Where: o.KYC.Name(), Status: "check", Note: "Confirm in the contract that the vendor stores and processes data in India."})
	} else {
		lines = append(lines, ResidencyLine{What: "Online account opening checks", Where: "Not connected (simulated or checked by staff)", Status: "none"})
	}
	lines = append(lines,
		ResidencyLine{What: "Email, SMS and WhatsApp", Where: "Not connected: messages kept in the outbox here", Status: "none", Note: "When connecting: email through an Indian region (Amazon SES Mumbai), an Indian SMS provider. WhatsApp messages pass through Meta's servers; send account details there only with the client's consent."},
	)
	return lines
}

func trimNote(s string) string {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	return s
}
