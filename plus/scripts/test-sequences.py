#!/usr/bin/env python3
"""
listmonk-plus: end-to-end test of sequences (engine + campaign-based steps, M2-M5).

Runs against the local dev environment with Mailhog. Start Listmonk with a fast
worker first:  PLUS_WORKER_INTERVAL=2s plus/scripts/dev-restart.sh

Creates its own lists/sequences/subscribers (suffix @plus-test.example) and
leaves them in place for inspection; re-running is safe.

Usage:
    LISTMONK_API_USER=plus-dev LISTMONK_API_TOKEN=... python3 -I plus/scripts/test-sequences.py
"""
import email
import email.policy
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = os.environ.get("LISTMONK_URL", "http://localhost:9000")
MAILHOG = os.environ.get("MAILHOG_URL", "http://localhost:8025")
AUTH = "token {}:{}".format(os.environ["LISTMONK_API_USER"], os.environ["LISTMONK_API_TOKEN"])
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
RUN = uuid.uuid4().hex[:6]
WAIT = 15  # seconds to wait for the worker (interval should be ~2s)
UUID_RE = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"


# --- helpers -----------------------------------------------------------------

class HTTPError(Exception):
    def __init__(self, code, body):
        super().__init__("HTTP {}: {}".format(code, body))
        self.code, self.body = code, body


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def http(method, path, body=None, form=None, auth=True, base=ROOT, raw=False, follow=True):
    headers = {"Authorization": AUTH} if auth else {}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    elif form is not None:
        data = urllib.parse.urlencode(form, doseq=True).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    req = urllib.request.Request(path if path.startswith("http") else base + path, data=data, method=method, headers=headers)
    opener = urllib.request.build_opener() if follow else urllib.request.build_opener(NoRedirect)
    try:
        with opener.open(req) as r:
            out, status, loc = r.read(), r.status, r.headers.get("Location")
    except urllib.error.HTTPError as e:
        if not follow and e.code in (301, 302, 303, 307, 308):
            return e.code, "", e.headers.get("Location")
        raise HTTPError(e.code, e.read()[:400].decode(errors="replace"))
    if raw:
        return status, out.decode(errors="replace"), loc
    try:
        return json.loads(out)
    except ValueError:
        return out


def sql(query):
    out = subprocess.run(
        ["docker", "compose", "-f", os.path.join(REPO, "dev/docker-compose.yml"), "exec", "-T", "db",
         "psql", "-U", "listmonk-dev", "-d", "listmonk-dev", "-At", "-F", "\t", "-c", query],
        check=True, capture_output=True, text=True).stdout
    return [line.split("\t") for line in out.strip().splitlines() if line]


def addr(tag):
    return "{}-{}@plus-test.example".format(tag, RUN)


def sub_id(e):
    return int(sql("SELECT id FROM subscribers WHERE email = '{}'".format(e))[0][0])


def raw_mails(to):
    q = urllib.parse.urlencode({"kind": "to", "query": to, "limit": 50})
    return list(reversed(http("GET", "/api/v2/search?" + q, auth=False, base=MAILHOG)["items"]))


def mails(to):
    """Messages in Mailhog for an address, oldest first: [(subject, html_body, parsed_message)]."""
    out = []
    for it in raw_mails(to):
        msg = email.message_from_string(it["Raw"]["Data"], policy=email.policy.default)
        part = msg.get_body(preferencelist=("html", "plain"))
        out.append((str(msg["Subject"]), part.get_content() if part else "", msg))
    return out


def wait_until(fn, timeout=WAIT):
    deadline = time.time() + timeout
    while True:
        v = fn()
        if v or time.time() > deadline:
            return v
        time.sleep(0.5)


def wait_mails(to, n, timeout=WAIT):
    return wait_until(lambda: (lambda m: m if len(m) >= n else None)(mails(to)), timeout) or mails(to)


def enrollments(seq_id, sid):
    return sql("SELECT status, next_step, COALESCE(cancel_reason,''), source FROM plus_enrollments "
               "WHERE sequence_id = {} AND subscriber_id = {} ORDER BY id".format(seq_id, sid))


RESULTS = []


def check(name, ok, detail=""):
    RESULTS.append(bool(ok))
    print("{}  {}".format("PASS" if ok else "FAIL", name))
    if not ok and detail != "":
        print("      " + str(detail)[:500])


def subscribe_form(e, list_uuid, name="Tester"):
    http("POST", "/subscription/form", form={"email": e, "name": name, "l": [list_uuid]}, auth=False)


def link(html, pattern):
    m = re.search(r'href="([^"]*' + pattern + r')"', html)
    return m.group(1) if m else None


def next_url(html):
    return link(html, r"/plus/seq/next/" + UUID_RE + "/" + UUID_RE)


def path_of(url):
    return urllib.parse.urlparse(url).path if url else "/x"


def make_list(name, optin):
    return http("POST", "/api/lists", {"name": "{} {}".format(name, RUN), "type": "public", "optin": optin})["data"]


def add_step(seq_id, delay, subject):
    return http("POST", "/api/plus/sequences/{}/steps".format(seq_id), {"delay": delay, "subject": subject})["data"]


single = make_list("plus-seq single", "single")
double = make_list("plus-seq double", "double")
other = make_list("plus-seq other", "single")
print("Run {}: lists {} (single), {} (double), {} (other)\n".format(RUN, single["id"], double["id"], other["id"]))

# --- building a sequence -----------------------------------------------------------

seq = http("POST", "/api/plus/sequences", {"name": "Welcome " + RUN, "list_id": single["id"]})["data"]
S = seq["id"]
try:
    http("PUT", "/api/plus/sequences/{}".format(S), {"status": "active"})
    check("1  can't activate a sequence without steps", False, "activation succeeded")
except HTTPError as e:
    check("1  can't activate a sequence without steps", e.code == 400, e)

for delay, n in (("0", 1), ("3 seconds", 2), ("1 hour", 3)):
    seq = add_step(S, delay, "Step {} {}".format(n, RUN))
steps = seq["steps"]
check("2  three steps added, each with its own campaign",
      [s["position"] for s in steps] == [1, 2, 3] and [s["delay_seconds"] for s in steps] == [0, 3, 3600]
      and len({s["campaign_id"] for s in steps}) == 3, steps)
camp_ids = [s["campaign_id"] for s in steps]
c1 = http("GET", "/api/campaigns/{}".format(camp_ids[0]))["data"]
check("3  step campaign is a draft on the sequence's list, default template",
      c1["status"] == "draft" and [l["id"] for l in c1["lists"]] == [single["id"]] and c1["template_id"], c1)

try:
    add_step(S, "-5 minutes", "bad")
    check("4  negative delay rejected", False)
except HTTPError as e:
    check("4  negative delay rejected", e.code == 400, e)

# Edit step 1's content like on the campaign screen: personalised, a tracked link.
body1 = ('<p>Hi {{ .Subscriber.Name }}, this is step 1.</p>'
         '<p><a href="https://example.com/docs@TrackLink">Read the docs</a></p>'
         '<p><a href="{{ SequenceNextURL }}">Get the next email now</a></p>')
http("PUT", "/api/campaigns/{}".format(camp_ids[0]), {
    "name": c1["name"], "subject": c1["subject"], "from_email": c1["from_email"], "content_type": "richtext",
    "messenger": "email", "template_id": c1["template_id"], "body": body1, "lists": [other["id"]]})
c1 = http("GET", "/api/campaigns/{}".format(camp_ids[0]))["data"]
check("5  editing a step campaign works, but its list stays the sequence's list",
      "this is step 1" in c1["body"] and [l["id"] for l in c1["lists"]] == [single["id"]], c1["lists"])

http("PUT", "/api/plus/sequences/{}".format(S), {"status": "active"})

seq2 = http("POST", "/api/plus/sequences", {"name": "Second auto " + RUN, "list_id": single["id"]})["data"]
add_step(seq2["id"], "1 day", "Second " + RUN)
try:
    http("PUT", "/api/plus/sequences/{}".format(seq2["id"]), {"status": "active"})
    check("6  D5: second automated sequence on the same list is refused", False)
except HTTPError as e:
    check("6  D5: second automated sequence on the same list is refused", e.code == 409, e)
http("PUT", "/api/plus/sequences/{}".format(seq2["id"]), {"status": "active", "trigger_enabled": False})
check("7  D5: a manual-only sequence on the same list is allowed", True)

# --- safeguards and visibility (M5) ----------------------------------------------------

for name, fn in (
    ("8  can't start a step campaign", lambda: http("PUT", "/api/campaigns/{}/status".format(camp_ids[0]), {"status": "running"})),
    ("9  can't schedule a step campaign", lambda: http("PUT", "/api/campaigns/{}/status".format(camp_ids[0]), {"status": "scheduled"})),
    ("10 can't delete a step campaign", lambda: http("DELETE", "/api/campaigns/{}".format(camp_ids[0]))),
):
    try:
        fn()
        check(name, False, "succeeded")
    except HTTPError as e:
        check(name, e.code == 400 and "sequence" in e.body, e)

listed = http("GET", "/api/campaigns?per_page=all")["data"]["results"]
check("11 step campaigns are hidden from the campaigns list/API",
      not ({c["id"] for c in listed} & set(camp_ids)), [c["id"] for c in listed if c["id"] in camp_ids])
q = urllib.parse.urlencode({"query": RUN, "per_page": 50})
check("12 ...and from search (used by Campaigns -> Analytics picker)",
      not [c for c in http("GET", "/api/campaigns?" + q)["data"]["results"] if c["id"] in camp_ids])
http("DELETE", "/api/campaigns?all=true&query=" + urllib.parse.quote("Step 1 " + RUN))
check("13 bulk delete-by-query doesn't touch step campaigns",
      sql("SELECT COUNT(*) FROM campaigns WHERE id = {}".format(camp_ids[0]))[0][0] == "1")
counts = http("GET", "/api/dashboard/counts")["data"]
real_total = int(sql("SELECT COUNT(*) FROM campaigns c WHERE NOT EXISTS (SELECT 1 FROM plus_sequence_steps s WHERE s.campaign_id = c.id)")[0][0])
check("14 dashboard campaign total excludes step campaigns", counts["campaigns"]["total"] <= real_total,
      (counts["campaigns"]["total"], real_total))
_, page, _ = http("GET", "/admin/campaigns/{}".format(camp_ids[0]), raw=True)
check("15 step campaign screen: banner, link to sequence, no Start/Schedule",
      'data-testid="plus-step-banner"' in page and "/admin/plus/sequences/{}".format(S) in page
      and "onStart" not in page and "sendLater" not in page, re.findall(r"onStart|sendLater", page))

# --- trigger, delays, "next now" link ------------------------------------------------------

a = addr("anna")
subscribe_form(a, single["uuid"], name="Anna")
m = wait_mails(a, 1)
check("16 subscribing triggers step 1 immediately", m and m[0][0] == "Step 1 " + RUN, [x[0] for x in m])
check("17 step 1 uses the edited campaign content, personalised", m and "Hi Anna, this is step 1" in m[0][1], m[0][1][:300] if m else "")
hdr = m[0][2] if m else {}
check("18 sent like a campaign: X-Listmonk-Campaign header + one-click unsubscribe headers",
      m and hdr["X-Listmonk-Campaign"] == c1["uuid"] and "/subscription/" in str(hdr["List-Unsubscribe"] or "")
      and str(hdr["List-Unsubscribe-Post"] or "") == "List-Unsubscribe=One-Click",
      {k: str(hdr[k]) for k in ("X-Listmonk-Campaign", "List-Unsubscribe", "List-Unsubscribe-Post")} if m else "")
url1 = next_url(m[0][1]) if m else None
check("19 email contains the SequenceNextURL link", bool(url1), m[0][1][:400] if m else "")

m = wait_mails(a, 2)
check("20 step 2 arrives after its 3-second delay", len(m) >= 2 and m[1][0] == "Step 2 " + RUN, [x[0] for x in m])
A = sub_id(a)
e = wait_until(lambda: (lambda r: r if r and r[-1][1] == "3" else None)(enrollments(S, A)))
check("21 now waiting for step 3 (1 hour)", e and e[-1][:2] == ["active", "3"], e)
check("22 the other (manual-only) sequence did not start", enrollments(seq2["id"], A) == [])
check("23 campaign 'Sent' counter counts sequence sends",
      sql("SELECT sent FROM campaigns WHERE id = {}".format(camp_ids[0]))[0][0] == "1")

url2 = next_url(m[1][1]) if len(m) >= 2 else None
_, page, _ = http("GET", path_of(url2), auth=False, raw=True)
check("24 next-now link: GET shows a button and changes nothing",
      'method="post"' in page and enrollments(S, A)[-1][:2] == ["active", "3"], page[:200])
_, page, _ = http("GET", path_of(url1), auth=False, raw=True)
check("25 link from an older email says 'already sent'", "Already sent" in page, page[:200])
http("POST", path_of(url2), auth=False, raw=True)
m = wait_mails(a, 3)
check("26 pressing the button sends step 3 right away", len(m) >= 3 and m[2][0] == "Step 3 " + RUN, [x[0] for x in m])
e = wait_until(lambda: (lambda r: r if r and r[-1][0] == "completed" else None)(enrollments(S, A)))
check("27 pass is completed after the last step", e and e[-1][0] == "completed", enrollments(S, A))
_, page, _ = http("GET", path_of(next_url(m[2][1]) if len(m) >= 3 else None), auth=False, raw=True)
check("28 link in the last email says it was the last one", "last one" in page, page[:200])
check("29 click recorded (skip_clicked_at + event)",
      sql("SELECT count(*) FROM plus_sends WHERE skip_clicked_at IS NOT NULL AND subscriber_id = {}".format(A))[0][0] == "1"
      and sql("SELECT count(*) FROM plus_events WHERE type = 'sequence.next_clicked' AND subscriber_id = {}".format(A))[0][0] == "1")

# --- tracking -> Sequences analytics ---------------------------------------------------------

msg1 = mails(a)[0][1]
px = re.search(r'src="([^"]*/campaign/' + UUID_RE + "/" + UUID_RE + r'/px\.png)"', msg1)
tl = link(msg1, r"/link/" + UUID_RE + "/" + UUID_RE + "/" + UUID_RE)
if px:
    http("GET", path_of(px.group(1)), auth=False, raw=True)
if tl:
    http("GET", path_of(tl), auth=False, raw=True, follow=False)
check("30 open pixel and tracked link present in the email", bool(px and tl), (bool(px), bool(tl)))
check("31 open and click recorded against the step's campaign",
      sql("SELECT (SELECT COUNT(*) FROM campaign_views WHERE campaign_id = {0}), (SELECT COUNT(*) FROM link_clicks WHERE campaign_id = {0})".format(camp_ids[0]))[0] == ["1", "1"])
_, page, _ = http("GET", "/admin/plus/sequences/analytics?id={}".format(S), raw=True)
row1 = re.search(r'data-testid="analytics-step">(.*?)</tr>', page, re.S)
cells = [re.sub(r"<[^>]+>|\s+", " ", c).strip() for c in re.findall(r"<td>(.*?)</td>", row1.group(1), re.S)] if row1 else []
check("32 Sequences -> Analytics shows step 1: 1 sent, 1 open, 1 click (totals; individual tracking off)",
      len(cells) >= 4 and cells[1].startswith("1") and cells[2].startswith("1") and cells[3].startswith("1"), cells)
q = urllib.parse.urlencode({"id": camp_ids[0]})
_, page, _ = http("GET", "/admin/campaigns/analytics?" + q, raw=True)
check("33 Campaigns -> Analytics refuses step campaigns", "Step 1 " + RUN not in page)

# --- re-entry rules (D3) -----------------------------------------------------------------------

b = addr("ben")
subscribe_form(b, single["uuid"], name="Ben")
wait_mails(b, 2)
B = sub_id(b)
subscribe_form(b, single["uuid"], name="Ben")
time.sleep(5)
r = enrollments(S, B)
check("34 subscribe again while active, rule 'continue' -> keeps going", len(r) == 1 and r[0][0] == "active", r)
http("PUT", "/api/plus/sequences/{}".format(S), {"reentry_while_active": "restart"})
subscribe_form(b, single["uuid"], name="Ben")
r = wait_until(lambda: (lambda x: x if len(x) == 2 else None)(enrollments(S, B)))
check("35 subscribe again while active, rule 'restart' -> restarts from step 1",
      r and r[0][0] == "cancelled" and r[0][2] == "restarted" and r[1][0] == "active", r)
m = wait_mails(b, 3)
check("36 restart re-sends step 1", len(m) >= 3 and m[2][0] == "Step 1 " + RUN, [x[0] for x in m])

# Native unsubscribe link (campaign's {{ UnsubscribeURL }} from the default template).
unsub = link(m[-1][1], r"/subscription/" + UUID_RE + "/" + UUID_RE) if m else None
check("37 email has Listmonk's own unsubscribe link", bool(unsub), m[-1][1][-600:] if m else "")
http("POST", path_of(unsub), form={"List-Unsubscribe": "One-Click"}, auth=False, raw=True)  # what Gmail sends
st = lambda lid: (sql("SELECT status FROM subscriber_lists WHERE subscriber_id = {} AND list_id = {}".format(B, lid)) or [[""]])[0][0]
check("38 one-click unsubscribe -> unsubscribed from the sequence's list", st(single["id"]) == "unsubscribed", st(single["id"]))
r = wait_until(lambda: (lambda x: x if x[-1][0] == "cancelled" else None)(enrollments(S, B)))
check("39 unsubscribing stops the sequence", r and r[-1][2] == "unsubscribed", enrollments(S, B))

subscribe_form(b, single["uuid"], name="Ben")
time.sleep(5)
check("40 re-subscribe after leaving, rule 'ignore' -> not enrolled again", len(enrollments(S, B)) == 2, enrollments(S, B))
http("PUT", "/api/plus/sequences/{}".format(S), {"reentry_after_exit": "restart"})
http("PUT", "/api/subscribers/lists", {"ids": [B], "action": "unsubscribe", "target_list_ids": [single["id"]]})
time.sleep(3)
subscribe_form(b, single["uuid"], name="Ben")
r = wait_until(lambda: (lambda x: x if len(x) == 3 else None)(enrollments(S, B)))
check("41 re-subscribe after leaving, rule 'restart' -> enrolled again", r and r[-1][0] == "active", r)

# --- double opt-in -------------------------------------------------------------------------------

dseq = http("POST", "/api/plus/sequences", {"name": "Double " + RUN, "list_id": double["id"]})["data"]
add_step(dseq["id"], "0", "Double welcome " + RUN)
http("PUT", "/api/plus/sequences/{}".format(dseq["id"]), {"status": "active"})
c = addr("chloe")
subscribe_form(c, double["uuid"], name="Chloe")
time.sleep(5)
C = sub_id(c)
check("42 double opt-in: not enrolled before confirming", enrollments(dseq["id"], C) == [], enrollments(dseq["id"], C))
c_uuid = sql("SELECT uuid FROM subscribers WHERE id = {}".format(C))[0][0]
http("POST", "/subscription/optin/{}".format(c_uuid), form={"confirm": "true", "l": [double["uuid"]]}, auth=False)
m = wait_until(lambda: [x for x in mails(c) if x[0] == "Double welcome " + RUN])
check("43 double opt-in: confirming enrolls and sends", bool(m), [x[0] for x in mails(c)])

# --- manual enrollment (D2) ------------------------------------------------------------------------

d1 = http("POST", "/api/subscribers", {"email": addr("d1"), "name": "D1", "status": "enabled",
                                       "lists": [single["id"]], "preconfirm_subscriptions": True})["data"]["id"]
d2 = http("POST", "/api/subscribers", {"email": addr("d2"), "name": "D2", "status": "enabled",
                                       "lists": [single["id"]]})["data"]["id"]
http("PUT", "/api/subscribers/lists", {"ids": [d2], "action": "unsubscribe", "target_list_ids": [single["id"]]})
res = http("POST", "/api/plus/sequences/{}/enrollments".format(seq2["id"]),
           {"subscriber_ids": [d1, d2, 99999999], "source": "manual"})["data"]
check("44 manual enroll by id: eligible enrolled, others skipped with reasons",
      res["enrolled"] == 1 and res["skipped"].get("unsubscribed") == 1 and res["skipped"].get("not_found") == 1, res)
res = http("POST", "/api/plus/sequences/{}/enrollments".format(seq2["id"]),
           {"subscriber_emails": [addr("anna"), "nobody-" + RUN + "@example.com"], "dry_run": True})["data"]
check("45 enroll by email, dry run: counts without saving",
      res["dry_run"] and res["enrolled"] == 1 and res["skipped"].get("not_found") == 1 and enrollments(seq2["id"], A) == [], res)
res = http("POST", "/api/plus/sequences/{}/enrollments".format(seq2["id"]), {"all": True, "exclude_other_sequences": True})["data"]
check("46 bulk enroll all: people in another sequence left out when asked",
      res["skipped"].get("in_other_sequence", 0) >= 1 and res["skipped"].get("already_active", 0) >= 1, res)
enr_id = sql("SELECT id FROM plus_enrollments WHERE sequence_id = {} AND subscriber_id = {}".format(seq2["id"], d1))[0][0]
http("DELETE", "/api/plus/sequences/{}/enrollments/{}".format(seq2["id"], enr_id))
check("47 admin can stop an enrollment", enrollments(seq2["id"], d1)[0][2] == "cancelled_by_admin")

# --- pause / resume -----------------------------------------------------------------------------------

http("PUT", "/api/plus/sequences/{}".format(S), {"status": "paused"})
p = addr("paul")
subscribe_form(p, single["uuid"], name="Paul")
time.sleep(5)
P = sub_id(p)
check("48 paused: a new subscriber isn't enrolled", enrollments(S, P) == [], enrollments(S, P))
res = http("POST", "/api/plus/sequences/{}/enrollments".format(S), {"subscriber_ids": [P]})["data"]
time.sleep(5)
check("49 paused: a manually enrolled person gets nothing yet", res["enrolled"] == 1 and mails(p) == [], [x[0] for x in mails(p)])
http("PUT", "/api/plus/sequences/{}".format(S), {"status": "active"})
m = wait_mails(p, 1)
check("50 resume: the overdue step goes out", m and m[0][0] == "Step 1 " + RUN, [x[0] for x in m])

# --- failure handling ------------------------------------------------------------------------------------

fseq = http("POST", "/api/plus/sequences", {"name": "Broken " + RUN, "list_id": single["id"], "trigger_enabled": False})["data"]
fstep = add_step(fseq["id"], "0", "Broken " + RUN)["steps"][0]
fc = http("GET", "/api/campaigns/{}".format(fstep["campaign_id"]))["data"]
http("PUT", "/api/campaigns/{}".format(fc["id"]), {
    "name": fc["name"], "subject": fc["subject"], "from_email": fc["from_email"], "content_type": "html",
    "messenger": "email", "template_id": fc["template_id"], "body": '<p>{{ index .Subscriber.Attribs "nope" "deeper" }}</p>'})
http("PUT", "/api/plus/sequences/{}".format(fseq["id"]), {"status": "active"})
http("POST", "/api/plus/sequences/{}/enrollments".format(fseq["id"]), {"subscriber_ids": [d1]})
r = wait_until(lambda: sql("SELECT sd.status, sd.attempts, (e.next_send_at > NOW() + INTERVAL '4 minutes')::TEXT "
                           "FROM plus_sends sd JOIN plus_enrollments e ON e.id = sd.enrollment_id WHERE e.sequence_id = {}".format(fseq["id"])))
check("51 a step that fails to render is marked failed and retried ~5 min later", r and r[0] == ["failed", "1", "true"], r)

# --- deleting ------------------------------------------------------------------------------------------------

http("PUT", "/api/plus/sequences/{}".format(fseq["id"]), {"status": "paused"})
http("DELETE", "/api/plus/sequences/{}".format(fseq["id"]))
check("52 deleting a sequence deletes its step campaigns",
      sql("SELECT COUNT(*) FROM campaigns WHERE id = {}".format(fc["id"]))[0][0] == "0")
try:
    http("DELETE", "/api/plus/sequences/{}".format(S))
    check("53 can't delete an active sequence", False)
except HTTPError as e:
    check("53 can't delete an active sequence", e.code == 400, e)

print("\n{}/{} passed".format(sum(RESULTS), len(RESULTS)))
sys.exit(0 if all(RESULTS) else 1)
