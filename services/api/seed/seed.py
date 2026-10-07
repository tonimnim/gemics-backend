#!/usr/bin/env python3
"""Seed a local Tonits stack with realistic, production-shaped data.

Drives the real API wherever it can (accounts, competitions, entries, draws,
match reports, screenshots, staff decisions) so the data has exactly the shape
production creates. Only rows that need an outside system (completed M-Pesa
payments) are written straight to PostgreSQL.

Development only. Run against an empty local database:

    python3 services/api/seed/seed.py

It writes the generated staff credentials to apps/admin/dev-admin.local.
"""
import hashlib
import io
import json
import os
import random
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone

API = os.environ.get("SEED_API_URL", "http://localhost:8090")
ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
COMPOSE = ["docker", "compose", "--env-file", os.path.join(ROOT, ".env.docker")]
GAMICS_ORG = "6a1c5e00-0000-4000-8000-000000000001"
rng = random.Random(20261008)


class APIError(Exception):
    def __init__(self, status, body):
        super().__init__(f"{status} {body}")
        self.status, self.body = status, body


def call(method, path, body=None, token=None, idempotent=False, expect=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method)
    req.add_header("Accept", "application/json")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if idempotent:
        req.add_header("Idempotency-Key", str(uuid.uuid4()))
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as err:
        raw = err.read().decode()
        if expect and err.code in expect:
            return None
        raise APIError(err.code, raw) from None


def sql(statement, *params):
    """Runs SQL in the stack's Postgres and returns rows as lists of strings."""
    script = statement
    for p in params:
        script = script.replace("%s", "'" + str(p).replace("'", "''") + "'", 1)
    out = subprocess.run(COMPOSE + ["exec", "-T", "postgres", "sh", "-c",
                         'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA -F "|"'],
                         input=script, capture_output=True, text=True, cwd=ROOT)
    if out.returncode != 0:
        raise RuntimeError(out.stderr)
    return [line.split("|") for line in out.stdout.strip().splitlines() if line]


def step(message):
    print(f"\n== {message}", flush=True)


# ---------------------------------------------------------------- people

KENYAN = ["Brian Otieno", "Achieng Wanjiru", "Kevin Kamau", "Faith Njeri", "Dennis Mwangi", "Mercy Atieno",
          "Collins Kiprop", "Sharon Chebet", "Victor Omondi", "Brenda Wairimu", "Ian Mutua", "Joy Akinyi",
          "Felix Kiptoo", "Esther Nyambura", "Allan Ouma", "Grace Wanjiku", "Samuel Kariuki", "Diana Moraa",
          "Eric Langat", "Purity Mumbi", "Moses Onyango", "Lilian Jepkosgei", "Paul Njoroge", "Winnie Adhiambo",
          "George Kibet", "Cynthia Wambui", "Peter Ochieng", "Nancy Kerubo", "Daniel Macharia", "Ruth Chepkoech",
          "Steve Waweru", "Mary Awino", "Tony Gitau", "Carol Nyokabi", "Erick Rotich", "Janet Mwende",
          "Kelvin Odhiambo", "Ann Wangari", "Martin Kimani", "Lucy Jeruto", "Hassan Ali", "Amina Yusuf",
          "Ibrahim Abdi", "Zainab Omar", "Kennedy Wekesa", "Sylvia Nafula", "Brian Simiyu", "Edwin Barasa",
          "Caleb Kipchumba", "Dorcas Wanza"]
INDIAN = ["Arjun Sharma", "Rohan Gupta", "Aditya Verma", "Karan Mehta", "Vikram Nair", "Rahul Iyer",
          "Ananya Reddy", "Priya Singh", "Siddharth Rao", "Neha Kapoor", "Aman Joshi", "Ishaan Malhotra"]
REGIONAL = [("Joseph Mukasa", "UG"), ("Ronald Ssemakula", "UG"), ("Juma Mwakyusa", "TZ"), ("Baraka Mollel", "TZ"),
            ("Emmanuel Okafor", "NG"), ("Tunde Adeyemi", "NG")]
TAGS = ["Striker", "Maestro", "Kingpin", "Wizard", "Phantom", "Titan", "Viper", "Sniper", "Falcon", "Panther",
        "Rocket", "Shadow", "Blaze", "Legend", "Rhino", "Simba", "Chui", "Duma", "Mamba", "Tusker"]
PHONE_PREFIX = {"KE": "+2547", "IN": "+919", "UG": "+2567", "TZ": "+2557", "NG": "+23480"}


def gamer_tag(name, used):
    first = name.split()[0]
    for _ in range(50):
        tag = rng.choice([
            f"{first}{rng.choice(TAGS)}", f"{rng.choice(TAGS)}_{first}", f"{first.lower()}{rng.randint(7, 99)}",
            f"{first}_{rng.choice(['KE', 'FC', '254', 'GOAT', 'OG'])}", f"{rng.choice(TAGS)}{rng.randint(10, 99)}"])
        if tag.lower() not in used and 3 <= len(tag) <= 24:
            used.add(tag.lower())
            return tag
    raise RuntimeError("no tag")


def konami_id():
    letters = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
    return "-".join("".join(rng.choice(letters) for _ in range(4)) for _ in range(3))


def phone(country):
    prefix = PHONE_PREFIX[country]
    digits = {"KE": 8, "IN": 9, "UG": 8, "TZ": 8, "NG": 7}[country]
    return prefix + "".join(str(rng.randint(0, 9)) for _ in range(digits))


def register(name, country, password):
    used = register.used
    username = gamer_tag(name, used)
    kid = konami_id()
    session = call("POST", "/v1/auth/register",
                   {"username": username, "konamiId": kid, "password": password, "deviceName": "Seed"})
    person = {"name": name, "username": username, "konamiId": kid, "password": password, "country": country,
              "token": session["accessToken"], "id": session["player"]["id"]}
    accounts = call("GET", "/v1/me/game-accounts", token=person["token"])["data"]
    person["gameAccountId"] = accounts[0]["id"]
    call("PATCH", "/v1/me", {"displayName": name}, token=person["token"])
    return person


register.used = set()


def main():
    step("Staff")
    password = os.environ.get("SEED_PASSWORD") or hashlib.sha256(os.urandom(16)).hexdigest()[:16]
    admin = register("Tonits Admin", "KE", password)
    out = subprocess.run(COMPOSE + ["exec", "-T", "api", "gamics-staff", "grant", admin["konamiId"], "admin"],
                         capture_output=True, text=True, cwd=ROOT)
    print(out.stdout.strip() or out.stderr.strip())
    staff = [admin]
    for name, role in [("Wanjiku Support", "support"), ("Otieno Reviewer", "reviewer"),
                       ("Amina Operator", "operator"), ("Kariuki Finance", "admin")]:
        member = register(name, "KE", password)
        call("POST", "/v1/admin/staff", {"konamiId": member["konamiId"], "role": role}, token=admin["token"])
        member["role"] = role
        staff.append(member)
        print(f"  {role:9} {member['username']} ({member['konamiId']})")
    with open(os.path.join(ROOT, "apps", "admin", "dev-admin.local"), "w") as f:
        f.write("# Local development staff accounts (gitignored). Not for production.\n")
        f.write(f"# Every account below uses PASSWORD={password}\n")
        for member in staff:
            f.write(f"{member.get('role', 'admin'):9} KONAMI_ID={member['konamiId']}  # {member['name']}\n")

    step("Players")
    people = [(n, "KE") for n in KENYAN] + [(n, "IN") for n in INDIAN] + REGIONAL
    players = []
    for name, country in people:
        p = register(name, country, hashlib.sha256(name.encode()).hexdigest()[:14])
        if rng.random() < 0.85:
            call("PUT", "/v1/me/phone", {"phoneNumber": phone(country)}, token=p["token"])
        players.append(p)
    print(f"  {len(players)} players")
    state = {"staff": staff, "players": players, "password": password}
    json.dump(state, open("/tmp/tonits-seed-state.json", "w"))
    return state


if __name__ == "__main__":
    main()
