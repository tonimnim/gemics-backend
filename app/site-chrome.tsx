import Link from "next/link";
import { BrandMark } from "./brand-mark";
import { ArrowIcon } from "./icons";

export function SiteHeader() {
  return (
    <header className="site-header">
      <div className="shell site-header-inner">
        <Link className="brand" href="/" aria-label="Home">
          <BrandMark />
        </Link>
        <nav className="site-nav" aria-label="Primary">
          <Link href="/tournaments">Tournaments</Link>
          <Link className="nav-secondary" href="/#how-it-works">How it works</Link>
          <Link className="button button-glass button-small" href="/#app">
            Get the app
          </Link>
        </nav>
      </div>
    </header>
  );
}

export function SiteFooter() {
  return (
    <footer className="site-footer">
      <div className="shell">
        <div className="footer-row">
          <Link className="brand" href="/" aria-label="Home">
            <BrandMark />
          </Link>
          <nav aria-label="Footer">
            <Link href="/tournaments">Tournaments</Link>
              <Link href="/#how-it-works">How it works</Link>
            <Link href="/#app">
              Get the app <ArrowIcon className="inline-icon" />
            </Link>
          </nav>
        </div>
        <div className="footer-legal">
          <p>
            eFootball is a trademark of Konami Digital Entertainment. Tonits is an independent platform and is not
            affiliated with or endorsed by Konami.
          </p>
          <span>© 2026</span>
        </div>
      </div>
    </footer>
  );
}
