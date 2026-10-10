"""Stage 3: a live bracket with checked-in matches, blind reports, screenshots and reviews."""
import hashlib
import io
import json
import sys
import time
import urllib.request

sys.path.insert(0, __import__("os").path.dirname(__file__))
from PIL import Image, ImageDraw, ImageFont  # noqa: E402
from seed import GAMICS_ORG, APIError, call, rng, step  # noqa: E402

state = json.load(open("/tmp/tonits-seed-state.json"))
admin = state["staff"][0]["token"]
by_id = {p["id"]: p for p in state["players"]}
orgs = f"/v1/organizations/{GAMICS_ORG}/competitions"
launch = state.get("launch", {})


def font(size):
    for path in ["/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
                 "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf"]:
        try:
            return ImageFont.truetype(path, size)
        except OSError:
            pass
    return ImageFont.load_default()


def screenshot(home, away, home_score, away_score):
    """An in-game style full-time screen, the kind players upload as evidence."""
    img = Image.new("RGB", (1600, 720), (12, 22, 48))
    draw = ImageDraw.Draw(img)
    for y in range(720):
        shade = int(20 + 30 * y / 720)
        draw.line([(0, y), (1600, y)], fill=(10, shade, 60 + shade))
    draw.rectangle([0, 0, 1600, 70], fill=(0, 0, 0))
    draw.text((40, 18), "eFootball   FULL TIME", font=font(30), fill=(255, 255, 255))
    draw.text((330, 250), home[:14], font=font(54), fill=(255, 255, 255), anchor="mm")
    draw.text((1270, 250), away[:14], font=font(54), fill=(255, 255, 255), anchor="mm")
    draw.text((800, 260), f"{home_score}  -  {away_score}", font=font(150), fill=(255, 214, 0), anchor="mm")
    draw.text((800, 470), "Match Stats   Possession  52% : 48%   Shots  11 : 7", font=font(32),
              fill=(200, 210, 230), anchor="mm")
    buf = io.BytesIO()
    img.save(buf, "PNG")
    return buf.getvalue()


def upload(player, data):
    digest = hashlib.sha256(data).hexdigest()
    intent = call("POST", "/v1/evidence/uploads", {"mediaType": "image/png", "byteSize": len(data), "sha256": digest},
                  token=player["token"], idempotent=True)["data"]
    req = urllib.request.Request(intent["uploadUrl"], data=data, method="PUT")
    for k, v in intent["requiredHeaders"].items():
        req.add_header(k, v)
    urllib.request.urlopen(req, timeout=60).read()
    call("POST", f"/v1/evidence/uploads/{intent['id']}/complete", {}, token=player["token"], idempotent=True)
    for _ in range(60):
        status = call("GET", f"/v1/evidence/uploads/{intent['id']}", token=player["token"])["data"]
        if status.get("ready"):
            return intent["id"]
        if status.get("status") in ("failed", "rejected", "expired"):
            raise RuntimeError(f"evidence {status}")
        time.sleep(1)
    raise RuntimeError("evidence never became ready")


def report(match_id, player, home, away):
    """The player submits the result; the opponent then confirms or rejects it."""
    body = {"homeScore": home, "awayScore": away, "declarationAccepted": True}
    if home == away:
        body["tiebreak"] = {"type": "penalties", "homeScore": 4, "awayScore": 3}
    return call("POST", f"/v1/matches/{match_id}/score-reports", body, token=player["token"], idempotent=True)


def answer(match_id, player, decision):
    return call("POST", f"/v1/matches/{match_id}/score-reports/confirmation", {"decision": decision},
                token=player["token"], idempotent=True)


def send_screenshot(match_id, player, evidence_id):
    return call("POST", f"/v1/matches/{match_id}/score-reports/screenshot", {"evidenceId": evidence_id},
                token=player["token"], idempotent=True)


def main():
    step("Live bracket: Tonits Launch Cup")
    cid = launch["id"]
    c = call("POST", f"{orgs}/{cid}/transitions", {"status": "check_in"}, token=admin)["data"]
    print("  ->", c["status"])
    draw = {"seedingPolicy": "random", "expectedStatus": "check_in",
            "config": {"bestOf": 1, "checkInLeadMinutes": 180, "checkInGraceMinutes": 60,
                       "resultWindowMinutes": 1440, "roundIntervalMinutes": 1440}}
    call("POST", f"{orgs}/{cid}/draws", draw, token=admin, idempotent=True)
    c = call("POST", f"{orgs}/{cid}/transitions", {"status": "running"}, token=admin)["data"]
    print("  ->", c["status"])

    # Each entrant finds their round-one match.
    matches = {}
    for pid in launch["entrants"]:
        p = by_id[pid]
        for m in call("GET", "/v1/me/matches", token=p["token"])["data"]:
            if m.get("competitionId") == cid or m.get("competition", {}).get("id") == cid:
                matches.setdefault(m["id"], set()).add(pid)
    print(f"  {len(matches)} round-one matches")

    plans = ["agree", "agree", "agree", "dispute", "dispute", "dispute", "pending", "checked_in"]
    rng.shuffle(plans)
    reviews = 0
    for (match_id, pids), plan in zip(sorted(matches.items()), plans):
        room = call("GET", f"/v1/matches/{match_id}", token=by_id[next(iter(pids))]["token"])["data"]
        home_entry = room.get("home", {}) or {}
        away_entry = room.get("away", {}) or {}
        home_player = by_id.get(home_entry.get("captainUserId") or home_entry.get("playerId") or "")
        away_player = by_id.get(away_entry.get("captainUserId") or away_entry.get("playerId") or "")
        if not home_player or not away_player:
            ids = list(pids)
            home_player, away_player = by_id[ids[0]], by_id[ids[1]]
        for p in (home_player, away_player):
            call("POST", f"/v1/matches/{match_id}/check-ins", {}, token=p["token"], idempotent=True)
        if plan == "checked_in":
            continue
        h, a = rng.randint(0, 4), rng.randint(0, 4)
        if h == a:
            h += 1
        report(match_id, home_player, h, a)
        if plan == "pending":
            continue
        if plan == "agree":
            answer(match_id, away_player, "confirm")
            continue
        # A dispute: the away side rejects the result, then each side sends a
        # screenshot; the away one is edited to the reverse score.
        answer(match_id, away_player, "reject")
        home_shot = upload(home_player, screenshot(home_player["username"], away_player["username"], h, a))
        away_shot = upload(away_player, screenshot(home_player["username"], away_player["username"], a, h))
        send_screenshot(match_id, home_player, home_shot)
        send_screenshot(match_id, away_player, away_shot)
        reviews += 1
    print(f"  plans: {sorted(plans)}; {reviews} disputes sent to Gamics review")


if __name__ == "__main__":
    main()
