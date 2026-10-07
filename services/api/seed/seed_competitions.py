"""Stage 2: competitions in every state, with real registrations."""
import json
import sys
from datetime import datetime, timedelta, timezone

sys.path.insert(0, __import__("os").path.dirname(__file__))
from seed import GAMICS_ORG, APIError, call, rng, sql, step  # noqa: E402

state = json.load(open("/tmp/tonits-seed-state.json"))
admin = state["staff"][0]["token"]
players = state["players"]
kenyans = [p for p in players if p["country"] == "KE"]
indians = [p for p in players if p["country"] == "IN"]
now = datetime.now(timezone.utc)
orgs = f"/v1/organizations/{GAMICS_ORG}/competitions"


def iso(delta):
    return (now + delta).replace(microsecond=0).isoformat()


def create(name, fmt="single_elimination", max_entries=16, fee=0, prize=0, opens=-1, closes=5, starts=7, desc=""):
    body = {"name": name, "description": desc, "gameId": "efootball-mobile", "format": fmt,
            "maxEntries": max_entries, "entryFeeMinor": fee * 100, "currency": "KES",
            "prizeAmountMinor": prize * 100, "prizeFunding": "organizer" if prize else "none",
            "registrationOpensAt": iso(timedelta(days=opens)), "registrationClosesAt": iso(timedelta(days=closes)),
            "startsAt": iso(timedelta(days=starts))}
    competition = call("POST", orgs, body, token=admin)["data"]
    print(f"  {name}: draft")
    return competition


def move(competition, *statuses, reason=""):
    for status in statuses:
        body = {"status": status, **({"reason": reason} if reason else {})}
        competition = call("POST", f"{orgs}/{competition['id']}/transitions", body, token=admin)["data"]
    print(f"    -> {competition['status']}")
    return competition


def register_free(competition, entrants):
    for p in entrants:
        call("POST", f"/v1/competitions/{competition['id']}/registrations", {"gameAccountId": p["gameAccountId"]},
             token=p["token"], idempotent=True)
    print(f"    {len(entrants)} registered")


def register_paid(competition, entrants, fee):
    """A completed M-Pesa payment and its entry, as the payment callback writes them."""
    for i, p in enumerate(entrants):
        phone = "2547" + "".join(str(rng.randint(0, 9)) for _ in range(8))
        sql("""WITH entry AS (
                 INSERT INTO competition_entries(competition_id,display_name,captain_user_id)
                 VALUES (%s,%s,%s) RETURNING id),
               member AS (
                 INSERT INTO entry_members(entry_id,competition_id,user_id,game_account_id)
                 SELECT id,%s,%s,%s FROM entry RETURNING entry_id)
               INSERT INTO payment_intents(user_id,competition_id,game_account_id,entry_display_name,amount_minor,
                 phone_e164,request_ip,idempotency_key,request_hash,status,merchant_request_id,checkout_request_id,
                 entry_id,provider_receipt,provider_result_code,completed_at,created_at)
               SELECT %s,%s,%s,%s,%s,%s,'41.90.0.1','seed-'||gen_random_uuid(),repeat('a',64),'succeeded',
                 'merchant-'||gen_random_uuid(),'ws_CO_'||gen_random_uuid(),id,%s,'0',
                 now()-make_interval(hours=>%s),now()-make_interval(hours=>%s) FROM member
               JOIN entry ON entry.id=member.entry_id""",
            competition["id"], p["name"], p["id"], competition["id"], p["id"], p["gameAccountId"],
            p["id"], competition["id"], p["gameAccountId"], p["name"], fee * 100, phone,
            "SJ" + "".join(rng.choice("ABCDEFGHJKLMNPQRSTUVWXYZ0123456789") for _ in range(8)), i + 1, i + 1)
    print(f"    {len(entrants)} paid entries")


step("Competitions")
pool = kenyans[:]
rng.shuffle(pool)

c = create("December Showdown", max_entries=32, prize=10000, opens=20, closes=40, starts=42,
           desc="End-of-year knockout for the best eFootball players in Kenya.")

c = create("Kisumu Invitational", max_entries=16, prize=3000, opens=3, closes=10, starts=11,
           desc="Lakeside invitational. Registration opens soon.")
move(c, "published")

c = create("Nairobi Weekend Cup", max_entries=32, prize=5000, opens=-2, closes=4, starts=5,
           desc="Free entry. Top four share the prize, funded by Tonits.")
move(c, "published", "registration_open")
register_free(c, pool[:23])

c = create("Mombasa Masters", max_entries=16, fee=100, prize=8000, opens=-3, closes=3, starts=4,
           desc="KES 100 entry. Prize funded by Tonits.")
move(c, "published", "registration_open")
register_paid(c, pool[23:34], 100)

c = create("Mumbai eFootball Clash", max_entries=16, prize=0, opens=-1, closes=6, starts=7,
           desc="Free community cup open to every country.")
move(c, "published", "registration_open")
register_free(c, indians[:9] + [p for p in players if p["country"] in ("UG", "TZ", "NG")][:4])

c = create("Eldoret Open", max_entries=16, fee=200, prize=6000, opens=-5, closes=2, starts=3,
           desc="Cancelled: venue partner withdrew.")
move(c, "published", "registration_open")
register_paid(c, pool[34:40], 200)
state["eldoret"] = c["id"]

# Running bracket: 16 checked-in players, check-in open now so matches can be played.
c = create("Tonits Launch Cup", max_entries=16, prize=15000, opens=-4, closes=0.01, starts=0.03,
           desc="The first official Tonits knockout.")
move(c, "published", "registration_open")
launch = pool[:16]
register_free(c, launch)
state["launch"] = {"id": c["id"], "entrants": [p["id"] for p in launch]}

json.dump(state, open("/tmp/tonits-seed-state.json", "w"))
