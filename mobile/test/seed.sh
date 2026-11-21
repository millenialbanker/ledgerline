#!/bin/bash
# Starts a development server and prepares it for screenshots.cjs:
#   go build -o "$SP/ll" ./cmd/ledgerline && SP=... bash mobile/test/seed.sh
#   CLIENT="$(cat $SP/client.json)" OUT=/some/dir PLAYWRIGHT=/path/to/playwright node mobile/test/screenshots.cjs
# Made-up sample data only.
SP=${SP:-$(mktemp -d)}
B=http://127.0.0.1:18080
K="Authorization: Bearer dev-key-not-for-production"
cd "$SP"
LEDGERLINE_HOLIDAY_FETCH=off "$SP/ll" -dev -addr 127.0.0.1:18080 > "$SP/server.log" 2>&1 &
for i in $(seq 1 60); do curl -s $B/healthz >/dev/null && break; sleep 1; done
curl -s -X POST $B/v1/dev/sample -H "$K" -H "X-Actor: admin" -H 'Content-Type: application/json' -d '{"date":"2026-11-20","autopilot":true}' | head -c 300; echo
curl -s -X PUT $B/v1/mobile/settings -H "$K" -H "X-Actor: admin" -H 'Content-Type: application/json' -d '{"staff_on":true,"staff_roles":["Ops checker","Branch staff","Compliance officer","Admin"],"idle_minutes":5,"lock_seconds":60,"reauth_days":30,"client":{"on":true,"app_name":"Demo Invest","primary":"#0b5cad","accent":"#0f9d76","dp_reg":"IN-DP-000-2026","registered_office":"12 Example Road, Mumbai 400001","support_email":"support@example.in","support_phone":"+91 22 0000 0000","grievance_officer":"A. Sample","grievance_email":"grievance@example.in","grievance_phone":"+91 22 0000 0001","compliance_officer":"B. Sample, compliance@example.in","investor_charter_url":"https://www.example.in/investor-charter","risk_disclosure_url":"https://www.example.in/risk","website_url":"https://www.example.in"}}' | head -c 200; echo
UCC=$(curl -s "$B/v1/clients?date=2026-11-20" -H "$K" -H "X-Actor: admin" | python3 -c 'import json,sys; cs=json.load(sys.stdin); print(next(c["ucc"] for c in cs if c["status"]=="Active" and not c.get("dp_only") and c.get("segments")))')
# Payouts waiting for a checker, prepared by Autopilot (the sample day
# approves them itself otherwise).
for p in payouts third_party pledges; do curl -s -X POST $B/v1/autopilot/policies -H "$K" -H "X-Actor: admin" -H 'Content-Type: application/json' -d "{\"id\":\"$p\",\"mode\":\"Suggest\",\"params\":{}}" >/dev/null; done
for u in BH1002 BH1003 BH1004 BH1005; do curl -s -X POST $B/v1/payouts -H "$K" -H "X-Actor: priya.nair" -H 'Content-Type: application/json' -d "{\"date\":\"2026-11-20\",\"ucc\":\"$u\",\"amount\":\"2500\"}" >/dev/null; done
curl -s -X POST $B/v1/autopilot/run -H "$K" -H "X-Actor: admin" -H 'Content-Type: application/json' -d '{"date":"2026-11-20","only":"payouts"}' >/dev/null
echo "client $UCC"
curl -s -X POST "$B/v1/clients/$UCC/portal" -H "$K" -H "X-Actor: farhan.khan" -H 'Content-Type: application/json' -d '{"family":[],"declared":false}' > "$SP/client.json"
cat "$SP/client.json"; echo
