#!/usr/bin/env python3
"""
listmonk-plus: end-to-end test of the sequence engine (milestone 2).

Runs against the local dev environment with Mailhog. Start Listmonk with a fast
worker first:  PLUS_WORKER_INTERVAL=2s plus/scripts/dev-restart.sh

Creates its own templates/lists/sequences/subscribers (suffix @plus-test.example)
and leaves them in place for inspection; re-running is safe.

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


# --- helpers -----------------------------------------------------------------

class HTTPError(Exception):
    def __init__(self, code, body):
        super().__init__("HTTP {}: {}".format(code, body))
        self.code, self.body = code, body


def http(method, path, body=None, form=None, auth=True, base=ROOT, raw=False, follow=True):
    headers = {"Authorization": AUTH} if auth else {}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    elif form is not None:
        data = urllib.parse.urlencode(form, doseq=True).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    req = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    opener = urllib.request.build_opener() if follow else urllib.request.build_opener(NoRedirect)
    try:
        with opener.open(req) as r:
            out = r.read()
            status = r.status
            loc = r.headers.get("Location")
    except urllib.error.HTTPError as e:
        if not follow and e.code in (301, 302, 303, 307, 308):
            return e.code, e.headers.get("Location")
        raise HTTPError(e.code, e.read()[:400].decode(errors="replace"))
    if raw:
        return status, out.decode(errors="replace"), loc
    try:
        return json.loads(out)
    except ValueError:
        return out


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


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


def mails(to):
    """Messages in Mailhog for an address, oldest first: [(subject, html_body)]."""
    q = urllib.parse.urlencode({"kind": "to", "query": to, "limit": 50})
    items = http("GET", "/api/v2/search?" + q, auth=False, base=MAILHOG)["items"]
    out = []
    for it in reversed(items):
        msg = email.message_from_string(it["Raw"]["Data"], policy=email.policy.default)
        part = msg.get_body(preferencelist=("html", "plain"))
        out.append((str(msg["Subject"]), part.get_content() if part else ""))
    return out


def wait_mails(to, n, timeout=WAIT):
    deadline = time.time() + timeout
    while True:
        m = mails(to)
        if len(m) >= n or time.time() > deadline:
            return m
        time.sleep(0.5)


def enrollments(seq_id, sid):
    return sql("SELECT status, next_step, COALESCE(cancel_reason,''), source FROM plus_enrollments "
               "WHERE sequence_id = {} AND subscriber_id = {} ORDER BY id".format(seq_id, sid))


def wait_until(fn, timeout=WAIT):
    deadline = time.time() + timeout
    while True:
        v = fn()
        if v or time.time() > deadline:
            return v
        time.sleep(0.5)


RESULTS = []


def check(name, ok, detail=""):
    RESULTS.append(bool(ok))
    print("{}  {}".format("PASS" if ok else "FAIL", name))
    if not ok and detail:
        print("      " + str(detail))


def subscribe_form(e, list_uuid, name="Tester"):
    http("POST", "/subscription/form", form={"email": e, "name": name, "l": [list_uuid]}, auth=False)


def next_url(html):
    m = re.search(r'href="([^"]*/plus/seq/next/[0-9a-f-]{36})"', html)
    return m.group(1) if m else None


# --- setup -------------------------------------------------------------------

def make_tpl(n, extra=""):
    body = ('<p>Hi {{ .Subscriber.Name }}, this is step ' + str(n) + '.</p>' + extra +
            '<p><a href="{{ SequenceNextURL }}">Get the next email now</a></p>')
    return http("POST", "/api/templates", {"name": "plus-test step {} {}".format(n, RUN), "type": "tx",
                                           "subject": "Step {} {}".format(n, RUN), "body": body})["data"]["id"]


def make_list(name, optin):
    return http("POST", "/api/lists", {"name": "{} {}".format(name, RUN), "type": "public", "optin": optin})["data"]


try:
    t1, t2, t3 = make_tpl(1), make_tpl(2), make_tpl(3)
    check("1  tx template using {{ SequenceNextURL }} can be saved", True)
except HTTPError as e:
    check("1  tx template using {{ SequenceNextURL }} can be saved", False, e)
    sys.exit(1)

single = make_list("plus-seq single", "single")
double = make_list("plus-seq double", "double")
print("Run {}: lists {} (single), {} (double); templates {}, {}, {}\n".format(
    RUN, single["id"], double["id"], t1, t2, t3))

# --- sequence building rules ---------------------------------------------------

seq = http("POST", "/api/plus/sequences", {"name": "Welcome " + RUN, "list_id": single["id"]})["data"]
S = seq["id"]
try:
    http("PUT", "/api/plus/sequences/{}".format(S), {"status": "active"})
    check("2  can't activate a sequence without steps", False, "activation succeeded")
except HTTPError as e:
    check("2  can't activate a sequence without steps", e.code == 400, e)

for tpl, delay in ((t1, "0"), (t2, "3 seconds"), (t3, "1 hour")):
    seq = http("POST", "/api/plus/sequences/{}/steps".format(S), {"template_id": tpl, "delay": delay})["data"]
check("3  three steps added in order", [s["position"] for s in seq["steps"]] == [1, 2, 3]
      and [s["delay_seconds"] for s in seq["steps"]] == [0, 3, 3600], seq["steps"])

try:
    http("POST", "/api/plus/sequences/{}/steps".format(S), {"template_id": t1, "delay": "-5 minutes"})
    check("4  negative delay rejected", False)
except HTTPError as e:
    check("4  negative delay rejected", e.code == 400, e)

http("PUT", "/api/plus/sequences/{}".format(S), {"status": "active"})

seq2 = http("POST", "/api/plus/sequences", {"name": "Second auto " + RUN, "list_id": single["id"]})["data"]
http("POST", "/api/plus/sequences/{}/steps".format(seq2["id"]), {"template_id": t1, "delay": "1 day"})
try:
    http("PUT", "/api/plus/sequences/{}".format(seq2["id"]), {"status": "active"})
    check("5  D5: second automated sequence on the same list is refused", False)
except HTTPError as e:
    check("5  D5: second automated sequence on the same list is refused", e.code == 409, e)
http("PUT", "/api/plus/sequences/{}".format(seq2["id"]), {"status": "active", "trigger_enabled": False})
check("6  D5: a manual-only sequence on the same list is allowed", True)

# --- trigger, delays, "next now" link -----------------------------------------

a = addr("anna")
subscribe_form(a, single["uuid"], name="Anna")
m = wait_mails(a, 1)
check("7  subscribing triggers step 1 immediately", len(m) >= 1 and m[0][0] == "Step 1 " + RUN, m)
check("8  personalised with the subscriber's name", m and "Hi Anna" in m[0][1], m[0][1][:200] if m else "")
url1 = next_url(m[0][1]) if m else None
check("9  email contains a working SequenceNextURL", bool(url1), m[0][1][:300] if m else "")

m = wait_mails(a, 2)
check("10 step 2 arrives after its 3-second delay", len(m) >= 2 and m[1][0] == "Step 2 " + RUN, [x[0] for x in m])
A = sub_id(a)
e = wait_until(lambda: (lambda r: r if r and r[-1][1] == "3" else None)(enrollments(S, A)))
check("11 now waiting for step 3 (1 hour)", e and e[-1][:2] == ["active", "3"], e)
check("12 the other (manual-only) sequence did not start", enrollments(seq2["id"], A) == [])

url2 = next_url(m[1][1]) if len(m) >= 2 else None
path2 = urllib.parse.urlparse(url2).path if url2 else "/plus/seq/next/x"
status, page, _ = http("GET", path2, auth=False, raw=True)
check("13 next-now link: GET shows a confirmation button and changes nothing",
      'method="post"' in page and enrollments(S, A)[-1][:2] == ["active", "3"], page[:200])

status, page, _ = http("GET", urllib.parse.urlparse(url1).path, auth=False, raw=True)
check("14 link from an older email says 'already sent'", "Already sent" in page, page[:200])

http("POST", path2, auth=False, raw=True)
m = wait_mails(a, 3)
check("15 pressing the button sends step 3 right away", len(m) >= 3 and m[2][0] == "Step 3 " + RUN,
      [x[0] for x in m])
e = wait_until(lambda: (lambda r: r if r and r[-1][0] == "completed" else None)(enrollments(S, A)))
check("16 pass is completed after the last step", e and e[-1][0] == "completed", enrollments(S, A))
status, page, _ = http("GET", urllib.parse.urlparse(next_url(m[2][1])).path, auth=False, raw=True)
check("17 link in the last email says it was the last one", "last one" in page, page[:200])
check("18 click recorded (skip_clicked_at + event)",
      sql("SELECT count(*) FROM plus_sends WHERE skip_clicked_at IS NOT NULL AND subscriber_id = {}".format(A))[0][0] == "1"
      and sql("SELECT count(*) FROM plus_events WHERE type = 'sequence.next_clicked' AND subscriber_id = {}".format(A))[0][0] == "1")

# --- re-entry rules (D3) -----------------------------------------------------------

b = addr("ben")
subscribe_form(b, single["uuid"], name="Ben")
wait_mails(b, 2)
B = sub_id(b)
subscribe_form(b, single["uuid"], name="Ben")  # subscribes again while active (continue)
time.sleep(5)
r = enrollments(S, B)
check("19 subscribe again while active, rule 'continue' -> keeps going", len(r) == 1 and r[0][0] == "active", r)

http("PUT", "/api/plus/sequences/{}".format(S), {"reentry_while_active": "restart"})
subscribe_form(b, single["uuid"], name="Ben")
r = wait_until(lambda: (lambda x: x if len(x) == 2 else None)(enrollments(S, B)))
check("20 subscribe again while active, rule 'restart' -> restarts from step 1",
      r and r[0][0] == "cancelled" and r[0][2] == "restarted" and r[1][0] == "active", r)
m = wait_mails(b, 3)
check("21 restart re-sends step 1", len(m) >= 3 and m[2][0] == "Step 1 " + RUN, [x[0] for x in m])

http("PUT", "/api/subscribers/lists", {"ids": [B], "action": "unsubscribe", "target_list_ids": [single["id"]]})
r = wait_until(lambda: (lambda x: x if x[-1][0] == "cancelled" else None)(enrollments(S, B)))
check("22 unsubscribing cancels the sequence", r and r[-1][2] == "unsubscribed", r)

subscribe_form(b, single["uuid"], name="Ben")  # re-subscribe after leaving (ignore)
time.sleep(5)
check("23 re-subscribe after leaving, rule 'ignore' -> not enrolled again", len(enrollments(S, B)) == 2,
      enrollments(S, B))

http("PUT", "/api/plus/sequences/{}".format(S), {"reentry_after_exit": "restart"})
http("PUT", "/api/subscribers/lists", {"ids": [B], "action": "unsubscribe", "target_list_ids": [single["id"]]})
time.sleep(3)
subscribe_form(b, single["uuid"], name="Ben")
r = wait_until(lambda: (lambda x: x if len(x) == 3 else None)(enrollments(S, B)))
check("24 re-subscribe after leaving, rule 'restart' -> enrolled again", r and r[-1][0] == "active", r)

# --- double opt-in -----------------------------------------------------------------

dseq = http("POST", "/api/plus/sequences", {"name": "Double " + RUN, "list_id": double["id"]})["data"]
http("POST", "/api/plus/sequences/{}/steps".format(dseq["id"]), {"template_id": t1, "delay": "0",
                                                                    "subject": "Double welcome " + RUN})
http("PUT", "/api/plus/sequences/{}".format(dseq["id"]), {"status": "active"})
c = addr("chloe")
subscribe_form(c, double["uuid"], name="Chloe")
time.sleep(5)
C = sub_id(c)
check("25 double opt-in: not enrolled before confirming", enrollments(dseq["id"], C) == [], enrollments(dseq["id"], C))
c_uuid = sql("SELECT uuid FROM subscribers WHERE id = {}".format(C))[0][0]
http("POST", "/subscription/optin/{}".format(c_uuid), form={"confirm": "true", "l": [double["uuid"]]}, auth=False)
m = wait_until(lambda: [x for x in mails(c) if x[0] == "Double welcome " + RUN])
check("26 double opt-in: confirming enrolls and sends (subject override)", bool(m), [x[0] for x in mails(c)])

# --- manual enrollment (D2) ---------------------------------------------------------

d1 = http("POST", "/api/subscribers", {"email": addr("d1"), "name": "D1", "status": "enabled",
                                       "lists": [single["id"]], "preconfirm_subscriptions": True})["data"]["id"]
d2 = http("POST", "/api/subscribers", {"email": addr("d2"), "name": "D2", "status": "enabled",
                                       "lists": [single["id"]]})["data"]["id"]
http("PUT", "/api/subscribers/lists", {"ids": [d2], "action": "unsubscribe", "target_list_ids": [single["id"]]})
res = http("POST", "/api/plus/sequences/{}/enrollments".format(seq2["id"]),
           {"subscriber_ids": [d1, d2, 99999999], "source": "manual"})["data"]
check("27 manual enroll by id: eligible enrolled, others skipped with reasons",
      res["enrolled"] == 1 and res["skipped"].get("unsubscribed") == 1 and res["skipped"].get("not_found") == 1, res)

res = http("POST", "/api/plus/sequences/{}/enrollments".format(seq2["id"]), {"all": True})["data"]
check("28 bulk enroll all list members: already-enrolled skipped, unsubscribed skipped",
      res["skipped"].get("already_active", 0) >= 1 and res["skipped"].get("unsubscribed", 0) >= 1, res)
r = enrollments(seq2["id"], d1)
check("29 manual-only sequence: waits for its 1-day delay", r and r[0][:2] == ["active", "1"] and r[0][3] == "manual", r)

enr_id = sql("SELECT id FROM plus_enrollments WHERE sequence_id = {} AND subscriber_id = {}".format(seq2["id"], d1))[0][0]
http("DELETE", "/api/plus/sequences/{}/enrollments/{}".format(seq2["id"], enr_id))
check("30 admin can cancel an enrollment", enrollments(seq2["id"], d1)[0][2] == "cancelled_by_admin")

# --- pause / resume -------------------------------------------------------------------

http("PUT", "/api/plus/sequences/{}".format(S), {"status": "paused"})
p = addr("paul")
subscribe_form(p, single["uuid"], name="Paul")
time.sleep(5)
P = sub_id(p)
check("31 paused: new subscriber is not enrolled (trigger only fires for active sequences)",
      enrollments(S, P) == [], enrollments(S, P))
res = http("POST", "/api/plus/sequences/{}/enrollments".format(S), {"subscriber_ids": [P]})["data"]
time.sleep(5)
check("32 paused: manually enrolled person gets nothing yet", res["enrolled"] == 1 and mails(p) == [], mails(p))
http("PUT", "/api/plus/sequences/{}".format(S), {"status": "active"})
m = wait_mails(p, 1)
check("33 resume: the overdue step goes out", len(m) >= 1 and m[0][0] == "Step 1 " + RUN, m)

try:
    http("DELETE", "/api/plus/sequences/{}".format(S))
    check("34 can't delete an active sequence", False)
except HTTPError as e:
    check("34 can't delete an active sequence", e.code == 400, e)

# --- failure handling -------------------------------------------------------------------

# A template that fails at render time (indexing a missing attribute) -> retry is scheduled.
bad = http("POST", "/api/templates", {"name": "plus-test broken " + RUN, "type": "tx", "subject": "Broken " + RUN,
                                      "body": '<p>{{ index .Subscriber.Attribs "nope" "deeper" }}</p>'})["data"]["id"]
fseq = http("POST", "/api/plus/sequences", {"name": "Broken " + RUN, "list_id": single["id"], "trigger_enabled": False})["data"]
http("POST", "/api/plus/sequences/{}/steps".format(fseq["id"]), {"template_id": bad, "delay": "0"})
http("PUT", "/api/plus/sequences/{}".format(fseq["id"]), {"status": "active"})
http("POST", "/api/plus/sequences/{}/enrollments".format(fseq["id"]), {"subscriber_ids": [d1]})
r = wait_until(lambda: sql("SELECT sd.status, sd.attempts, (e.next_send_at > NOW() + INTERVAL '4 minutes')::TEXT, "
                           "LEFT(COALESCE(sd.error,''), 40) FROM plus_sends sd JOIN plus_enrollments e ON e.id = sd.enrollment_id "
                           "WHERE e.sequence_id = {}".format(fseq["id"])))
check("35 a failing step is marked failed and retried ~5 min later (not lost, not resent)",
      r and r[0][:3] == ["failed", "1", "true"], r)

# --- unsubscribe (milestone 4) -----------------------------------------------------------

def raw_mails(to):
    q = urllib.parse.urlencode({"kind": "to", "query": to, "limit": 50})
    return list(reversed(http("GET", "/api/v2/search?" + q, auth=False, base=MAILHOG)["items"]))


ut = http("POST", "/api/templates", {"name": "plus-test unsub " + RUN, "type": "tx", "subject": "Unsub test " + RUN,
                                     "body": '<p>Hi {{ .Subscriber.Name }}</p><p><a href="{{ SequenceUnsubscribeURL }}">Unsubscribe</a></p>'})["data"]["id"]
check("36 tx template using {{ SequenceUnsubscribeURL }} can be saved", True)
useq = http("POST", "/api/plus/sequences", {"name": "Unsub " + RUN, "list_id": single["id"], "trigger_enabled": False})["data"]
http("POST", "/api/plus/sequences/{}/steps".format(useq["id"]), {"template_id": ut, "delay": "0"})
http("POST", "/api/plus/sequences/{}/steps".format(useq["id"]), {"template_id": ut, "delay": "1 hour"})
http("PUT", "/api/plus/sequences/{}".format(useq["id"]), {"status": "active"})

page_html = http("GET", "/admin/plus/sequences/{}".format(useq["id"]), raw=True)[1]
check("37 editor knows which templates have an unsubscribe link",
      re.search(r'"id":{},"name":"[^"]*","has_unsub":true'.format(ut), page_html) is not None
      and re.search(r'"id":{},"name":"[^"]*","has_unsub":false'.format(t1), page_html) is not None)

ue = addr("ursula")
U = http("POST", "/api/subscribers", {"email": ue, "name": "Ursula", "status": "enabled",
                                      "lists": [single["id"], double["id"]], "preconfirm_subscriptions": True})["data"]["id"]
http("POST", "/api/plus/sequences/{}/enrollments".format(useq["id"]), {"subscriber_ids": [U]})
m = wait_until(lambda: [x for x in raw_mails(ue) if x["Content"]["Headers"].get("Subject", [""])[0] == "Unsub test " + RUN])
# Parse the raw message: the mail library folds long headers onto a second line,
# which Mailhog's own header list doesn't unfold (real mail providers do).
_parsed = email.message_from_string(m[0]["Raw"]["Data"], policy=email.policy.default) if m else {}
hdr = {k: [str(_parsed[k]).strip()] for k in ("List-Unsubscribe", "List-Unsubscribe-Post") if m and _parsed[k]}
body = next((b for subj, b in mails(ue) if subj == "Unsub test " + RUN), "")
um = re.search(r'href="([^"]*/plus/seq/unsubscribe/[0-9a-f-]{36})"', body)
unsub_url = um.group(1) if um else ""
check("38 email has an unsubscribe link", bool(unsub_url), body[:300])
check("39 email has one-click List-Unsubscribe headers pointing to the same URL",
      hdr.get("List-Unsubscribe", [""])[0] == "<{}>".format(unsub_url)
      and hdr.get("List-Unsubscribe-Post", [""])[0] == "List-Unsubscribe=One-Click", hdr.get("List-Unsubscribe"))

upath = urllib.parse.urlparse(unsub_url).path if unsub_url else "/plus/seq/unsubscribe/x"
_, pg, _ = http("GET", upath, auth=False, raw=True)
st = lambda lid: sql("SELECT status FROM subscriber_lists WHERE subscriber_id = {} AND list_id = {}".format(U, lid))[0][0]
check("40 GET on the unsubscribe link only asks for confirmation", 'method="post"' in pg and st(single["id"]) == "confirmed", st(single["id"]))

http("POST", upath, form={"List-Unsubscribe": "One-Click"}, auth=False, raw=True)  # what Gmail sends
check("41 one-click POST unsubscribes from the sequence's list only",
      st(single["id"]) == "unsubscribed" and st(double["id"]) == "confirmed", (st(single["id"]), st(double["id"])))
r = wait_until(lambda: (lambda x: x if x and x[-1][0] == "cancelled" else None)(enrollments(useq["id"], U)))
check("42 unsubscribing stops the sequence", r and r[-1][2] == "unsubscribed", enrollments(useq["id"], U))
_, pg, _ = http("GET", upath, auth=False, raw=True)
check("43 the link afterwards says they're unsubscribed", "unsubscribed" in pg.lower() and 'method="post"' not in pg, pg[:200])
code = None
try:
    http("GET", "/plus/seq/unsubscribe/00000000-0000-0000-0000-000000000000", auth=False, raw=True)
except HTTPError as e:
    code = e.code
check("44 unknown unsubscribe link -> 404 page", code == 404, code)

print("\n{}/{} passed".format(sum(RESULTS), len(RESULTS)))
sys.exit(0 if all(RESULTS) else 1)
