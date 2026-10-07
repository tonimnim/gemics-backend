# Tonits Admin

The staff dashboard: one operator tool for Tonits V1, gated by staff roles.
Built on [satnaing/shadcn-admin](https://github.com/satnaing/shadcn-admin)
(MIT, see `LICENSE`) with Vite, React 19, TanStack Router/Query and shadcn/ui.

## Run it

```sh
cp .env.example .env        # VITE_API_URL, default http://localhost:8090
npm install
npm run dev                 # http://localhost:5173
```

The API must allow the dashboard origin in `CORS_ALLOWED_ORIGINS`
(`http://localhost:5173` is in the defaults).

## Signing in

Staff sign in with the Konami ID and password of an ordinary Tonits account.
An account becomes staff when it holds a platform role. Create the first admin
once from the command line, after registering the account in the app:

```sh
docker compose --env-file .env.docker exec api gamics-staff grant <konami-id> admin
```

After that, admins grant and revoke roles on **Staff & roles**.

## Roles

| Role | Can |
|---|---|
| Support | Overview, create and run competitions, see registrations (with each entry's payment status and amount) |
| Reviewer | Support, plus result reviews and account verifications |
| Operator | Reviewer, plus revoking conduct strikes |
| Admin | Everything: finance totals, payment reviews, refunds, staff roles |

Only admins see money totals and revenue. The API enforces every permission;
the dashboard only hides what a role cannot use.
