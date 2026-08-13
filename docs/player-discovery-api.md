# Player discovery and rankings implementation

This slice implements the five public mobile routes described in
`mobile-api-requirements.md`. Every collection has a maximum page size of 50 and a
signed, filter-bound keyset cursor. The cursor is not an authorization token.

## Privacy contract

- Only `users.status = active` plus `player_profiles.discoverable = true` are public.
- A private, suspended, deleted or missing player returns the same `404` response.
- Public responses never read or serialize email, phone, birth date, payment data,
  refresh sessions, publisher player IDs, analytics consent or object-store keys.
- `avatarUrl` is present but `null` until the separate avatar service can mint a
  public/CDN URL. `avatar_object_key` is deliberately never returned.
- An opponent that is no longer discoverable is `null` in public match history.
- These responses use `Cache-Control: private, no-store`. Shared caching must not be
  enabled until profile visibility/suspension events can purge every affected key.

## Ranking projection

Migration `000007_player_discovery` adds `player_game_ratings`, immutable-in-practice
leaderboard snapshots and `publish_leaderboard_snapshot(...)`. Result confirmation
must update `player_game_ratings` in the same transaction as the confirmed match.
A scheduled projection worker should then publish both scopes, for example:

```sql
SELECT publish_leaderboard_snapshot('efootball-mobile', 'global', NULL);
SELECT publish_leaderboard_snapshot('efootball-mobile', 'country', 'KE');
```

The function serializes publishers with a transaction-scoped advisory lock, ranks
only active/discoverable players with at least one played match, records movement
against the previous ready snapshot and publishes the snapshot atomically. The API
never performs a window sort on a phone request. Keep snapshots for at least the
24-hour ranking-cursor lifetime before deleting old rows.

`GET /v1/rankings` returns an empty page and `snapshotAt: null` before the first
snapshot is published. Search can still return discoverable connected players; its
rating/rank fields are `null` when no genuine projection exists.

## History limitations kept explicit

Match history includes only a confirmed submission attached to a completed/forfeit
match. Competition records are derived from those confirmed results. `placement` is
returned as `null` until the progression engine writes a finalized row to
`competition_entry_placements`. No seed is presented as a finish.

## Integration

Call the modular route registrar once in `httpapi.New` after constructing `mux`:

```go
s.registerPlayerDiscoveryRoutes(mux)
```

Merge `services/api/openapi/player-discovery.paths.yaml` into the main OpenAPI 3.1
document. The fragment is intentionally separate to avoid overwriting concurrent
match/result API work.
