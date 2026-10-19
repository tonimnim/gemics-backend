// Public contact points, kept in one place so the footer, the legal pages and
// anything else that names them can never disagree.
export const site = {
  contactEmail: "support@tonits.com",
  socials: [
    { name: "TikTok", handle: "@tonits", href: "https://www.tiktok.com/@tonits" },
    { name: "Instagram", handle: "@tonits", href: "https://www.instagram.com/tonits" },
    { name: "YouTube", handle: "@tonits", href: "https://www.youtube.com/@tonits" },
    { name: "X", handle: "@tonits", href: "https://x.com/tonits" },
  ],
  // Store listings for the app download buttons. Google Play addresses an app
  // by its package name; the App Store needs the app's id once it is listed.
  stores: {
    googlePlay: "https://play.google.com/store/apps/details?id=com.ozone",
    appStore: "https://apps.apple.com/app/ozone/id0000000000",
  },
} as const;

export type SocialName = (typeof site.socials)[number]["name"];

export const legalPages = [
  { href: "/terms", label: "Terms" },
  { href: "/privacy", label: "Privacy" },
  { href: "/refunds", label: "Refunds" },
  { href: "/fair-play", label: "Fair play" },
] as const;
