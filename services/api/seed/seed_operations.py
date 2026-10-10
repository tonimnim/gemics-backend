"""Stage 4: refunds, payment reviews, verifications, decided reviews, strikes and a finished cup."""
import io
import json
import sys

sys.path.insert(0, __import__("os").path.dirname(__file__))
from PIL import Image, ImageDraw  # noqa: E402
from seed import GAMICS_ORG, APIError, call, rng, sql, step  # noqa: E402
from seed_matches import answer, font, report, upload  # noqa: E402

state = json.load(open("/tmp/tonits-seed-state.json"))
staff = {m.get("role", "admin"): m for m in state["staff"]}
admin = state["staff"][0]["token"]
reviewer = staff["reviewer"]["token"]
players = state["players"]
by_id = {p["id"]: p for p in players}
orgs = f"/v1/organizations/{GAMICS_ORG}/competitions"


def comp(name):
    return sql("select id from competitions where name=%s", name)[0][0]


step("Refunds")
call("POST", f"{orgs}/{state['eldoret']}/transitions",
     {"status": "cancelled", "reason": "Venue partner withdrew; every paid entry is refunded."}, token=admin)
print("  Eldoret Open cancelled: mandatory refunds created")
mombasa = comp("Mombasa Masters")
paid = [r[0] for r in sql("select captain_user_id from competition_entries where competition_id=%s", mombasa)]
notes = ["Travelling for a family event that weekend.", "My phone broke, I can't play.", ""]
for uid, note in zip(paid[:3], notes):
    call("POST", f"/v1/competitions/{mombasa}/registrations/me/withdrawal-requests",
         {"reasonCode": "player_withdrawal", "note": note}, token=by_id[uid]["token"], idempotent=True)
print("  3 withdrawal requests")
queue = call("GET", "/v1/admin/refunds?stage=action", token=admin)["data"]
decide = lambda r, body: call("POST", f"/v1/admin/refunds/{r['id']}/decisions", body, token=admin, idempotent=True)
eldoret = [r for r in queue if r["reasonCode"] == "competition_cancelled"]
withdrawals = [r for r in queue if r["reasonCode"] == "player_withdrawal"]
decide(eldoret[0], {"decision": "approve"})
decide(eldoret[1], {"decision": "approve"})
decide(eldoret[1], {"decision": "mark_succeeded", "providerReceipt": "SKR7Q2M9XA", "note": "Sent by M-Pesa B2C."})
decide(eldoret[2], {"decision": "approve"})
decide(eldoret[2], {"decision": "mark_failed", "note": "Recipient number is not registered for M-Pesa."})
decide(withdrawals[2], {"decision": "reject", "note": "Withdrawal requested after the draw was published."})
print("  decisions: approved, refunded, failed, rejected")

step("Payment reviews")
nairobi = comp("Mombasa Masters")
spare = [p for p in players if p["country"] == "KE" and p["id"] not in paid][-6:]
for p, status, desc in zip(spare, ["review", "review", "review", "failed", "failed", "pending"],
                           ["M-Pesa answer lost; status query timed out",
                            "Callback amount, phone or receipt mismatch",
                            "Repeated Daraja query requests failed",
                            "Request cancelled by user", "DS timeout user cannot be reached", None]):
    sql("""INSERT INTO payment_intents(user_id,competition_id,game_account_id,entry_display_name,amount_minor,
             phone_e164,request_ip,idempotency_key,request_hash,status,merchant_request_id,checkout_request_id,
             provider_result_code,provider_result_description,query_attempts,last_query_at,completed_at,created_at)
           VALUES (%s,%s,%s,%s,10000,%s,'41.90.0.1','seed-'||gen_random_uuid(),repeat('a',64),%s,
             'merchant-'||gen_random_uuid(),'ws_CO_'||gen_random_uuid(),%s,%s,%s,now()-interval '20 minutes',
             CASE WHEN %s='failed' THEN now()-interval '1 hour' END,now()-interval '2 hours')""",
        p["id"], nairobi, p["gameAccountId"], p["name"], "2547" + str(rng.randint(10000000, 99999999)), status,
        {"review": "", "failed": "1032", "pending": ""}[status], desc or "", {"review": 12, "failed": 2, "pending": 1}[status],
        status)
print("  3 in review, 2 failed, 1 pending")

step("Account verifications")


def profile_shot(p):
    img = Image.new("RGB", (1600, 720), (18, 24, 44))
    d = ImageDraw.Draw(img)
    d.rectangle([0, 0, 1600, 80], fill=(0, 0, 0))
    d.text((40, 22), "eFootball   Profile", font=font(30), fill=(255, 255, 255))
    d.text((120, 220), p["username"], font=font(64), fill=(255, 255, 255))
    d.text((120, 330), f"User ID  {p['konamiId']}", font=font(44), fill=(255, 214, 0))
    d.text((120, 420), f"Division {rng.randint(1, 5)}   Matches {rng.randint(120, 2400)}", font=font(36),
           fill=(200, 210, 230))
    buf = io.BytesIO()
    img.save(buf, "PNG")
    return buf.getvalue()


verifying = [p for p in players if p["country"] in ("KE", "IN")][40:47]
requests = []
for p, note in zip(verifying, ["Please verify my main account.", "", "Screenshot of my profile page.", "", "",
                               "This is my only account.", ""]):
    evidence = upload(p, profile_shot(p))
    r = call("POST", f"/v1/me/game-accounts/{p['gameAccountId']}/verification-requests",
             {"evidenceIds": [evidence], "note": note}, token=p["token"], idempotent=True)
    requests.append(r["data"]["id"] if "data" in r else r["id"])
for rid, decision, reason in [(requests[0], "approve", ""), (requests[1], "approve", ""),
                              (requests[2], "reject", "The User ID in the screenshot does not match this account.")]:
    call("POST", f"/v1/admin/game-account-verifications/{rid}/decisions", {"decision": decision, "reason": reason},
         token=reviewer, idempotent=True)
print(f"  {len(requests)} requests: 2 approved, 1 rejected, {len(requests) - 3} waiting")

step("Result review decision with a strike")
queued = call("GET", "/v1/admin/result-reviews?status=queued", token=reviewer)["data"]
detail = call("GET", f"/v1/admin/result-reviews/{queued[0]['id']}", token=reviewer)["data"]
away = detail["participants"]["away"]["captainUserId"]
call("POST", f"/v1/admin/result-reviews/{queued[0]['id']}/decisions",
     {"expectedVersion": detail["version"], "decision": "accept_home", "strikeUserIds": [away],
      "note": "Home screenshot shows the full-time score clearly; the away claim reverses it."},
     token=reviewer, idempotent=True)
print("  1 decided (home claim accepted, away player struck); 2 still queued")

step("A finished cup: October Kickoff")
c = call("POST", orgs, {"name": "October Kickoff", "description": "Four-player opener.", "gameId": "efootball-mobile",
                        "format": "single_elimination", "maxEntries": 4, "entryFeeMinor": 0,
                        "prizeAmountMinor": 200000, "prizeFunding": "organizer",
                        "rules": {"eligibility": {"allowedCountries": ["KE"]}},
                        "registrationOpensAt": __import__("datetime").datetime.now(__import__("datetime").timezone.utc)
                        .replace(microsecond=0).isoformat(),
                        "registrationClosesAt": (__import__("datetime").datetime.now(__import__("datetime").timezone.utc)
                        + __import__("datetime").timedelta(minutes=20)).replace(microsecond=0).isoformat(),
                        "startsAt": (__import__("datetime").datetime.now(__import__("datetime").timezone.utc)
                        + __import__("datetime").timedelta(minutes=40)).replace(microsecond=0).isoformat()},
         token=admin)["data"]
for s in ["published", "registration_open"]:
    call("POST", f"{orgs}/{c['id']}/transitions", {"status": s}, token=admin)
four = [p for p in players if p["country"] == "KE"][-4:]
for p in four:
    call("POST", f"/v1/competitions/{c['id']}/registrations", {"gameAccountId": p["gameAccountId"]},
         token=p["token"], idempotent=True)
call("POST", f"{orgs}/{c['id']}/transitions", {"status": "check_in"}, token=admin)
call("POST", f"{orgs}/{c['id']}/draws", {"seedingPolicy": "registration_order", "expectedStatus": "check_in",
     "config": {"bestOf": 1, "checkInLeadMinutes": 180, "checkInGraceMinutes": 60, "resultWindowMinutes": 1440,
                "roundIntervalMinutes": 1440}}, token=admin, idempotent=True)
call("POST", f"{orgs}/{c['id']}/transitions", {"status": "running"}, token=admin)


def play_open_matches():
    played = 0
    seen = set()
    for p in four:
        for m in call("GET", "/v1/me/matches", token=p["token"])["data"]:
            if m["id"] in seen or m.get("state") not in ("ready", "in_progress", "pending") \
                    or (m.get("competitionId") or m.get("competition", {}).get("id")) != c["id"]:
                continue
            seen.add(m["id"])
            room = call("GET", f"/v1/matches/{m['id']}", token=p["token"])["data"]
            ids = [x for x in (room.get("home") or {}, room.get("away") or {})]
            sides = [by_id.get(x.get("captainUserId") or x.get("playerId") or "") for x in ids]
            if None in sides:
                continue
            for s in sides:
                call("POST", f"/v1/matches/{m['id']}/check-ins", {}, token=s["token"], idempotent=True, expect={409})
            h, a = rng.randint(1, 4), rng.randint(0, 3)
            if h == a:
                a = max(0, a - 1) if a else 0
            report(m["id"], sides[0], h, a)
            answer(m["id"], sides[1], "confirm")
            played += 1
    return played


import time  # noqa: E402
total = 0
for _ in range(6):
    n = play_open_matches()
    total += n
    if n == 0:
        time.sleep(3)
        if play_open_matches() == 0:
            break
print(f"  {total} matches played")
try:
    call("POST", f"{orgs}/{c['id']}/transitions", {"status": "completed"}, token=admin)
    print("  -> completed")
except APIError as err:
    print("  could not complete yet:", err.body[:160])
