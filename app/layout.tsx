import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { headers } from "next/headers";
import "./globals.css";

const geistSans = Geist({ variable: "--font-geist-sans", subsets: ["latin"] });
const geistMono = Geist_Mono({ variable: "--font-geist-mono", subsets: ["latin"] });

export async function generateMetadata(): Promise<Metadata> {
  const requestHeaders = await headers();
  const host = requestHeaders.get("x-forwarded-host") ?? requestHeaders.get("host") ?? "gamics.io";
  const protocol = requestHeaders.get("x-forwarded-proto") ?? (host.startsWith("localhost") ? "http" : "https");
  const origin = `${protocol}://${host}`;
  const description = "Trusted eFootball Mobile competitions, player records and tournament operations built for African esports.";

  return {
    metadataBase: new URL(origin),
    title: "Gamics — Your game. Your name.",
    description,
    // Browsers cache favicons far more aggressively than page assets, and a
    // stale one survives an ordinary reload. Bump this when the mark changes.
    // PNG rather than SVG. Chrome renders an SVG favicon through a separate
    // rasterisation path that proved unreliable here, and Google's favicon
    // crawler and older Safari want a bitmap regardless. logo.svg remains the
    // scalable master for everything that is not a tab icon.
    icons: {
      icon: [
        { url: "/favicon-32.png?v=4", sizes: "32x32", type: "image/png" },
        { url: "/favicon-16.png?v=4", sizes: "16x16", type: "image/png" },
      ],
      shortcut: "/favicon-32.png?v=4",
      apple: "/apple-touch-icon.png?v=4",
    },
    openGraph: {
      title: "Gamics — Your game. Your name.",
      description,
      type: "website",
      images: [{ url: `${origin}/og.png`, width: 1200, height: 630, alt: "Gamics — Your game. Your name." }],
    },
    twitter: {
      card: "summary_large_image",
      title: "Gamics — Your game. Your name.",
      description,
      images: [`${origin}/og.png`],
    },
  };
}

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body className={`${geistSans.variable} ${geistMono.variable}`}>{children}</body>
    </html>
  );
}
