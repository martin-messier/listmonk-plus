#!/usr/bin/env python3
"""
listmonk-plus: end-to-end check that every subscription path produces the right
plus_events rows (plus/docs/001-sequences-design.md §3).

Runs against the local dev environment (dev/docker-compose.yml). Creates its own
test lists/subscribers (email suffix @plus-test.example) and leaves them in place
for inspection; re-running is safe.

Usage:
    LISTMONK_API_USER=plus-dev LISTMONK_API_TOKEN=... python3 -I plus/scripts/test-events.py
"""
import atexit
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = os.environ.get("LISTMONK_URL", "http://localhost:9000")
AUTH = "token {}:{}".format(os.environ["LISTMONK_API_USER"], os.environ["LISTMONK_API_TOKEN"])
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
RUN = uuid.uuid4().hex[:6]


# --- helpers -----------------------------------------------------------------

def http(method, path, body=None, form=None, auth=True, multipart=None):
    headers = {}
    data = None
    if auth:
        headers["Authorization"] = AUTH
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    elif form is not None:
        data = urllib.parse.urlencode(form, doseq=True).encode()
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    elif multipart is not None:
        boundary = "plusboundary" + RUN
        parts = []
        for name, (filename, content) in multipart.items():
            disp = 'form-data; name="{}"'.format(name)
            if filename:
                disp += '; filename="{}"'.format(filename)
            parts.append("--{}\r\nContent-Disposition: {}\r\n\r\n{}\r\n".format(boundary, disp, content))
        data = ("".join(parts) + "--{}--\r\n".format(boundary)).encode()
        headers["Content-Type"] = "multipart/form-data; boundary=" + boundary

    req = urllib.request.Request(ROOT + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req) as r:
            raw = r.read()
    except urllib.error.HTTPError as e:
        raise SystemExit("HTTP {} {} -> {}: {}".format(method, path, e.code, e.read()[:300]))
    try:
        return json.loads(raw)
    except ValueError:
        return raw


def sql(query):
    out = subprocess.run(
        ["docker", "compose", "-f", os.path.join(REPO, "dev/docker-compose.yml"), "exec", "-T", "db",
         "psql", "-U", "listmonk-dev", "-d", "listmonk-dev", "-At", "-F", "\t", "-c", query],
        check=True, capture_output=True, text=True).stdout
    return [line.split("\t") for line in out.strip().splitlines() if line]


def max_event_id():
    return int(sql("SELECT COALESCE(MAX(id), 0) FROM plus_events")[0][0])


def events_since(marker, sub_ids):
    ids = ",".join(str(i) for i in sub_ids)
    rows = sql("SELECT subscriber_id, list_id, type, payload FROM plus_events "
               "WHERE id > {} AND subscriber_id IN ({}) ORDER BY id".format(marker, ids))
    return [(int(s), int(l), t, json.loads(p)) for s, l, t, p in rows]


def sub_id(email):
    return int(sql("SELECT id FROM subscribers WHERE email = '{}'".format(email))[0][0])


def email(tag):
    return "{}-{}@plus-test.example".format(tag, RUN)


RESULTS = []


def check(name, marker, sub_ids, expected, wait=0.0):
    """expected: list of (subscriber_id, list_id, type) in order."""
    deadline = time.time() + wait
    while True:
        got = events_since(marker, sub_ids)
        simple = [(s, l, t) for s, l, t, _ in got]
        if simple == expected or time.time() >= deadline:
            break
        time.sleep(0.5)
    ok = simple == expected
    RESULTS.append(ok)
    print("{}  {}".format("PASS" if ok else "FAIL", name))
    if not ok:
        print("      expected: {}".format(expected))
        print("      got:      {}".format(simple))
    return got


# --- setup -------------------------------------------------------------------

CREATED_LISTS = []


def cleanup():
    """Delete the lists and subscribers this run created (unless KEEP_TEST_DATA=1)."""
    if os.environ.get("KEEP_TEST_DATA") == "1":
        return
    sql("DELETE FROM subscribers WHERE email LIKE '%-{}@plus-test.example'".format(RUN))
    for lid in CREATED_LISTS:
        sql("DELETE FROM lists WHERE id = {}".format(lid))
    sql("DELETE FROM plus_events WHERE subscriber_id IS NOT NULL AND subscriber_id NOT IN (SELECT id FROM subscribers)")
    mailhog = os.environ.get("MAILHOG_URL", "http://localhost:8025")
    q = urllib.parse.urlencode({"kind": "containing", "query": RUN, "limit": 1000})
    for it in json.load(urllib.request.urlopen(mailhog + "/api/v2/search?" + q))["items"]:
        urllib.request.urlopen(urllib.request.Request(mailhog + "/api/v1/messages/" + it["ID"], method="DELETE"))
    print("Cleaned up run {}".format(RUN))


atexit.register(cleanup)


def make_list(name, optin):
    lst = http("POST", "/api/lists", {"name": "{} {}".format(name, RUN), "type": "public", "optin": optin})["data"]
    CREATED_LISTS.append(lst["id"])
    return lst


single = make_list("plus-test single", "single")
double = make_list("plus-test double", "double")
S, D = single["id"], double["id"]
C, CF, U, R, RP = ("subscription.created", "subscription.confirmed", "subscription.unsubscribed",
                   "subscription.removed", "subscription.repeated")
print("Run {}: single-opt-in list {}, double-opt-in list {}\n".format(RUN, S, D))

# 1. Public form, new subscriber, single opt-in list.
m = max_event_id()
http("POST", "/subscription/form", form={"email": email("a"), "name": "A", "l": [single["uuid"]]}, auth=False)
a = sub_id(email("a"))
check("1  public form, new subscriber, single opt-in -> created", m, [a], [(a, S, C)])

# 2. Public form again, already subscribed -> repeated.
m = max_event_id()
http("POST", "/subscription/form", form={"email": email("a"), "name": "A", "l": [single["uuid"]]}, auth=False)
got = check("2  public form again (already subscribed) -> repeated", m, [a], [(a, S, RP)])
if got:
    print("      payload: {}".format(got[0][3]))

# 3. Public JSON API again, already subscribed -> repeated.
m = max_event_id()
http("POST", "/api/public/subscription", {"email": email("a"), "name": "A", "list_uuids": [single["uuid"]]}, auth=False)
got = check("3  public JSON API again (already subscribed) -> repeated", m, [a], [(a, S, RP)])
if got:
    print("      payload: {}".format(got[0][3]))

# 4. Public form, double opt-in list -> created (unconfirmed); then confirm -> confirmed.
m = max_event_id()
http("POST", "/subscription/form", form={"email": email("b"), "name": "B", "l": [double["uuid"]]}, auth=False)
b = sub_id(email("b"))
check("4a public form, double opt-in -> created (unconfirmed)", m, [b], [(b, D, C)])
b_uuid = sql("SELECT uuid FROM subscribers WHERE id = {}".format(b))[0][0]
m = max_event_id()
http("POST", "/subscription/optin/{}".format(b_uuid), form={"confirm": "true", "l": [double["uuid"]]}, auth=False)
check("4b double opt-in confirmation link -> confirmed", m, [b], [(b, D, CF)])

# 5. Admin API: create subscriber, preconfirmed, on both lists.
m = max_event_id()
c = http("POST", "/api/subscribers", {"email": email("c"), "name": "C", "status": "enabled",
                                      "lists": [S, D], "preconfirm_subscriptions": True})["data"]["id"]
check("5  admin API create (preconfirmed, 2 lists) -> created+confirmed x2", m, [c],
      [(c, S, C), (c, S, CF), (c, D, C), (c, D, CF)])

# 6. Admin edit of the subscriber with the same lists -> NO events.
m = max_event_id()
http("PUT", "/api/subscribers/{}".format(c), {"email": email("c"), "name": "C edited", "status": "enabled",
                                               "lists": [S, D]})
check("6  admin edit, same lists -> no events", m, [c], [])

# 7. Admin API: unsubscribe from single list.
m = max_event_id()
http("PUT", "/api/subscribers/lists", {"ids": [c], "action": "unsubscribe", "target_list_ids": [S]})
check("7  admin unsubscribe -> unsubscribed", m, [c], [(c, S, U)])

# 8. Re-subscribe through the public form after unsubscribing -> created (resubscribe), no repeated.
m = max_event_id()
http("POST", "/subscription/form", form={"email": email("c"), "name": "C", "l": [single["uuid"]]}, auth=False)
got = check("8  public form after unsubscribe -> created (resubscribe), no repeated", m, [c], [(c, S, C)])
if got:
    print("      payload: {}".format(got[0][3]))

# 9. Admin API: add existing subscriber to a list.
m = max_event_id()
d = http("POST", "/api/subscribers", {"email": email("d"), "name": "D", "status": "enabled", "lists": []})["data"]["id"]
http("PUT", "/api/subscribers/lists", {"ids": [d], "action": "add", "target_list_ids": [S], "status": "confirmed"})
check("9  admin add-to-list (confirmed) -> created+confirmed", m, [d], [(d, S, C), (d, S, CF)])

# 10. Admin API: remove from list.
m = max_event_id()
http("PUT", "/api/subscribers/lists", {"ids": [d], "action": "remove", "target_list_ids": [S]})
check("10 admin remove-from-list -> removed", m, [d], [(d, S, R)])

# 11. Bulk add by query.
m = max_event_id()
e1 = http("POST", "/api/subscribers", {"email": email("e1"), "name": "E1", "status": "enabled", "lists": []})["data"]["id"]
e2 = http("POST", "/api/subscribers", {"email": email("e2"), "name": "E2", "status": "enabled", "lists": []})["data"]["id"]
http("PUT", "/api/subscribers/query/lists", {
    "query": "subscribers.email LIKE '%-{}@plus-test.example' AND subscribers.email LIKE 'e%'".format(RUN),
    "action": "add", "target_list_ids": [S], "status": "unconfirmed"})
check("11 bulk add-to-list by query (2 subs) -> created x2", m, [e1, e2], [(e1, S, C), (e2, S, C)])

# 12. CSV import (async).
m = max_event_id()
csv = "email,name,attributes\n{},F1,{{}}\n{},F2,{{}}\n".format(email("f1"), email("f2"))
http("POST", "/api/import/subscribers", multipart={
    "params": (None, json.dumps({"mode": "subscribe", "subscription_status": "confirmed", "delim": ",",
                                 "lists": [D], "overwrite": False})),
    "file": ("plus-test.csv", csv)})
for _ in range(60):
    if http("GET", "/api/import/subscribers")["data"]["status"] in ("finished", "failed", "stopped"):
        break
    time.sleep(0.5)
f1, f2 = sub_id(email("f1")), sub_id(email("f2"))
check("12 CSV import (2 subs, confirmed) -> created+confirmed x2", m, [f1, f2],
      [(f1, D, C), (f1, D, CF), (f2, D, C), (f2, D, CF)], wait=5)

# 13. Blocklist -> unsubscribed on every list.
m = max_event_id()
http("PUT", "/api/subscribers/{}/blocklist".format(c))
check("13 blocklist -> unsubscribed on all lists", m, [c], [(c, S, U), (c, D, U)])

# 14. Delete subscriber -> removed on every list.
m = max_event_id()
http("DELETE", "/api/subscribers/{}".format(b))
check("14 delete subscriber -> removed", m, [b], [(b, D, R)])

print("\n{}/{} passed".format(sum(RESULTS), len(RESULTS)))
sys.exit(0 if all(RESULTS) else 1)
