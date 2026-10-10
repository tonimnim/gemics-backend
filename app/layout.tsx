import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { headers } from "next/headers";
import "./globals.css";
import { Motion } from "./motion";

const geistSans = Geist({ variable: "--font-geist-sans", subsets: ["latin"] });
const geistMono = Geist_Mono({ variable: "--font-geist-mono", subsets: ["latin"] });

export const viewport: Viewport = {
  themeColor: "#07081a",
};

// Marks the document as scripted before first paint, so content that animates
// in on scroll only starts hidden when the script that reveals it can run. If
// the motion script has not started within four seconds (a failed or blocked
// bundle), the flag comes off again and everything shows without animation.
const scriptedFlag =
  "var d=document.documentElement;d.classList.add('js');" +
  "setTimeout(function(){if(!d.classList.contains('motion-ready'))d.classList.remove('js')},4000)";

export async function generateMetadata(): Promise<Metadata> {
  const requestHeaders = await headers();
  const host = requestHeaders.get("x-forwarded-host") ?? requestHeaders.get("host") ?? "gamics.io";
  const protocol = requestHeaders.get("x-forwarded-proto") ?? (host.startsWith("localhost") ? "http" : "https");
  const origin = `${protocol}://${host}`;
  const description = "eFootball Mobile tournaments. Enter, play, and settle every score.";

  return {
    metadataBase: new URL(origin),
    title: "Tonits — Your game. Your name.",
    description,
    // Browsers cache favicons far more aggressively than page assets, and a
    // stale one survives an ordinary reload. Bump this when the mark changes.
    // PNG rather than SVG. Chrome renders an SVG favicon through a separate
    // rasterisation path that proved unreliable here, and Google's favicon
    // crawler and older Safari want a bitmap regardless. logo.svg remains the
    // scalable master for everything that is not a tab icon.
    icons: {
      icon: [
        { url: "/favicon-32.png?v=5", sizes: "32x32", type: "image/png" },
        { url: "/favicon-16.png?v=5", sizes: "16x16", type: "image/png" },
      ],
      shortcut: "/favicon-32.png?v=5",
      apple: "/apple-touch-icon.png?v=5",
    },
    openGraph: {
      title: "Tonits — Your game. Your name.",
      description,
      type: "website",
      images: [{ url: `${origin}/og.png`, width: 1200, height: 630, alt: "Tonits — Your game. Your name." }],
    },
    twitter: {
      card: "summary_large_image",
      title: "Tonits — Your game. Your name.",
      description,
      images: [`${origin}/og.png`],
    },
  };
}

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className={`${geistSans.variable} ${geistMono.variable}`}>
        <script dangerouslySetInnerHTML={{ __html: scriptedFlag }} />
        {children}
        <Motion />
      </body>
    </html>
  );
}
