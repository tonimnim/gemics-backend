import Link from "next/link";
import { BrandMark } from "./brand-mark";
import { ArrowIcon, InstagramIcon, TikTokIcon, XIcon, YouTubeIcon } from "./icons";
import { type SocialName, legalPages, site } from "./lib/site";

const socialIcons: Record<SocialName, typeof XIcon> = {
  TikTok: TikTokIcon,
  Instagram: InstagramIcon,
  YouTube: YouTubeIcon,
  X: XIcon,
};

export function SiteHeader() {
  return (
    <header className="site-header">
      <div className="shell site-header-inner">
        <Link className="brand" href="/" aria-label="Home">
          <BrandMark />
        </Link>
        <nav className="site-nav" aria-label="Primary">
          <Link href="/tournaments">Tournaments</Link>
          <Link href="/rankings">Rankings</Link>
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
            <Link href="/rankings">Rankings</Link>
            <Link href="/#how-it-works">How it works</Link>
            <Link href="/#app">
              Get the app <ArrowIcon className="inline-icon" />
            </Link>
          </nav>
        </div>
        <div className="footer-mid">
          <nav className="footer-legal-links" aria-label="Legal">
            {legalPages.map((page) => (
              <Link href={page.href} key={page.href}>
                {page.label}
              </Link>
            ))}
          </nav>
          <ul className="socials" aria-label="Follow us">
            {site.socials.map((social) => {
              const Icon = socialIcons[social.name];
              return (
                <li key={social.name}>
                  <a
                    className="social"
                    href={social.href}
                    target="_blank"
                    rel="noopener noreferrer"
                    aria-label={`Tonits on ${social.name}`}
                    title={`${social.name} ${social.handle}`}
                  >
                    <Icon />
                  </a>
                </li>
              );
            })}
          </ul>
        </div>
        <div className="footer-legal">
          <p>
            eFootball is a trademark of Konami Digital Entertainment. Tonits is an independent platform and is not
            affiliated with or endorsed by Konami.
          </p>
          <span>© 2026 Tonits</span>
        </div>
      </div>
    </footer>
  );
}
