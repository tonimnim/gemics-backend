import type { Metadata } from "next";
import Link from "next/link";
import { type Competition, loadCompetitions } from "../lib/competitions";
import { PageHead } from "../page-head";
import { SiteFooter, SiteHeader } from "../site-chrome";
import { FeaturedTournament, TournamentRow } from "../tournament-row";

export const metadata: Metadata = {
  title: "Tournaments — Tonits",
};

type Group = {
  key: string;
  label: string;
  title: string;
  statuses: string[];
  newestFirst?: boolean;
};

const groups: Group[] = [
  { key: "open", label: "Open", title: "Open for entry", statuses: ["registration_open", "check_in"] },
  { key: "upcoming", label: "Upcoming", title: "Coming up", statuses: ["published"] },
  { key: "live", label: "Underway", title: "Underway", statuses: ["running"] },
  { key: "finished", label: "Finished", title: "Finished", statuses: ["completed"], newestFirst: true },
];

function startTime(competition: Competition) {
  const time = new Date(competition.startsAt).getTime();
  return Number.isNaN(time) ? Number.MAX_SAFE_INTEGER : time;
}

function inGroup(group: Group, competitions: Competition[]) {
  const members = competitions.filter((competition) => group.statuses.includes(competition.status));
  return members.sort((a, b) => (group.newestFirst ? startTime(b) - startTime(a) : startTime(a) - startTime(b)));
}

export default async function Tournaments({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const [{ data, reachable }, params] = await Promise.all([loadCompetitions(), searchParams]);
  const requested = typeof params.status === "string" ? params.status : "all";
  const active = groups.find((group) => group.key === requested) ?? null;
  const visible = (active ? [active] : groups)
    .map((group) => ({ group, members: inGroup(group, data) }))
    .filter(({ members }) => members.length > 0);

  // The event of the page: the next open tournament to start, else the next announced one.
  const featured = active ? null : (inGroup(groups[0], data)[0] ?? inGroup(groups[1], data)[0] ?? null);
  const openCount = inGroup(groups[0], data).length;

  return (
    <>
      <SiteHeader />

      <main>
        <PageHead
          title="Tournaments"
          meta={reachable && data.length > 0 ? `${data.length} total · ${openCount} open for entry` : null}
        >
          {reachable && data.length > 0 && (
            <nav className="filters" aria-label="Filter tournaments">
              <Link className="filter" href="/tournaments" aria-current={active ? undefined : "page"} scroll={false}>
                All
                <span>{data.length}</span>
              </Link>
              {groups.map((group) => (
                <Link
                  className="filter"
                  href={`/tournaments?status=${group.key}`}
                  aria-current={active?.key === group.key ? "page" : undefined}
                  key={group.key}
                  scroll={false}
                >
                  {group.label}
                  <span>{inGroup(group, data).length}</span>
                </Link>
              ))}
            </nav>
          )}
        </PageHead>

        <div className="shell">
          {!reachable && <p className="empty-note">Tournaments are unavailable right now. Try again shortly.</p>}

          {reachable && visible.length === 0 && <p className="empty-note">Nothing here right now. Check back soon.</p>}

          {featured && <FeaturedTournament competition={featured} />}

          {visible.map(({ group, members }) => (
            <section className="board-group" aria-labelledby={`group-${group.key}`} key={group.key}>
              <header className="board-group-head" data-reveal>
                <h2 id={`group-${group.key}`}>{group.title}</h2>
                <span>{members.length}</span>
              </header>
              <ol className="board">
                {members.map((competition, index) => (
                  <TournamentRow competition={competition} index={index} key={competition.id} />
                ))}
              </ol>
            </section>
          ))}
        </div>
      </main>

      <SiteFooter />
    </>
  );
}
