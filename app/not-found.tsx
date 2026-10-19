import Link from "next/link";
import { PageHead } from "./page-head";
import { SiteFooter, SiteHeader } from "./site-chrome";

export default function NotFound() {
  return (
    <>
      <SiteHeader />
      <main>
        <PageHead title="Not found" />
        <div className="shell">
          <p className="empty-note">
            That page doesn&apos;t exist, or it isn&apos;t public. <Link href="/tournaments">See the tournaments</Link>
          </p>
        </div>
      </main>
      <SiteFooter />
    </>
  );
}
