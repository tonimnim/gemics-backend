import Link from "next/link";
import { ArrowIcon } from "./icons";
import type { LegalDoc } from "./lib/legal-docs";
import { legalPages } from "./lib/site";
import { PageHead } from "./page-head";
import { SiteFooter, SiteHeader } from "./site-chrome";

/** One legal document: contents on the left, the text in a reading column, the other policies after it. */
export function LegalPage({ doc, path }: { doc: LegalDoc; path: string }) {
  const others = legalPages.filter((page) => page.href !== path);

  return (
    <>
      <SiteHeader />

      <main>
        <PageHead title={doc.title} meta={`Last updated ${doc.updated}`} />

        <div className="shell legal-layout">
          <nav className="legal-toc" aria-label="On this page">
            <p>On this page</p>
            <ol>
              {doc.sections.map((section) => (
                <li key={section.id}>
                  <a href={`#${section.id}`}>{section.title}</a>
                </li>
              ))}
            </ol>
          </nav>

          <article className="legal-body">
            <div className="legal-intro">{doc.intro}</div>
            {doc.sections.map((section, index) => (
              <section className="legal-section" id={section.id} data-legal-section key={section.id}>
                <h2>
                  <span>{String(index + 1).padStart(2, "0")}</span>
                  {section.title}
                </h2>
                {section.body}
              </section>
            ))}

            <aside className="legal-others" aria-label="Other policies">
              {others.map((page) => (
                <Link className="legal-other" href={page.href} key={page.href}>
                  {page.label}
                  <ArrowIcon className="inline-icon" />
                </Link>
              ))}
            </aside>
          </article>
        </div>
      </main>

      <SiteFooter />
    </>
  );
}
