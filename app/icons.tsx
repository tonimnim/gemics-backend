// Stroke icons drawn on a 24-unit grid, inlined so they take currentColor.
type IconProps = { className?: string };

function Icon({ className, children }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

export const ArrowIcon = ({ className }: IconProps) => (
  <Icon className={className}>
    <path d="M5 12h14" />
    <path d="m13 6 6 6-6 6" />
  </Icon>
);

export const TicketIcon = ({ className }: IconProps) => (
  <Icon className={className}>
    <path d="M3 8a2 2 0 0 0 2-2h14a2 2 0 0 0 2 2v2a2 2 0 0 0 0 4v2a2 2 0 0 0-2 2H5a2 2 0 0 0-2-2v-2a2 2 0 0 0 0-4Z" />
    <path d="M14 6v2.5M14 11v2M14 15.5V18" />
  </Icon>
);

export const ControllerIcon = ({ className }: IconProps) => (
  <Icon className={className}>
    <path d="M7 7h10a5 5 0 0 1 4.9 6l-.6 3a3 3 0 0 1-5.1 1.5L14.5 16h-5l-1.7 1.5A3 3 0 0 1 2.7 16l-.6-3A5 5 0 0 1 7 7Z" />
    <path d="M7.5 10.5v3M6 12h3" />
    <path d="M15.5 11h.01M17.5 13h.01" />
  </Icon>
);

export const ShieldCheckIcon = ({ className }: IconProps) => (
  <Icon className={className}>
    <path d="M12 3 4.5 6v5.5c0 4.4 3.1 8.3 7.5 9.5 4.4-1.2 7.5-5.1 7.5-9.5V6Z" />
    <path d="m8.8 12.2 2.2 2.2 4.4-4.6" />
  </Icon>
);

// The stores' own marks, in their colours, for the app download buttons.
export const GooglePlayIcon = ({ className }: IconProps) => (
  <svg className={className} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
    <defs>
      <clipPath id="google-play-triangle">
        <path d="M4.6 2.3c-.6.3-1 1-1 1.8v15.8c0 .8.4 1.5 1 1.8.6.3 1.3.3 1.9-.1l13.6-7.9c.7-.4 1.1-1.1 1.1-1.7s-.4-1.3-1.1-1.7L6.5 2.4c-.6-.4-1.3-.4-1.9-.1Z" />
      </clipPath>
    </defs>
    <g clipPath="url(#google-play-triangle)">
      <path fill="#00A0FF" d="M3 1 12.6 12 3 23Z" />
      <path fill="#00D46A" d="M3 1h4l9.4 7.6L12.6 12Z" />
      <path fill="#FF3A44" d="M3 23h4l9.4-7.6L12.6 12Z" />
      <path fill="#FFC400" d="M12.6 12 16.4 8.6 23 12 16.4 15.4Z" />
    </g>
  </svg>
);

export const AppStoreIcon = ({ className }: IconProps) => (
  <svg className={className} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
    <defs>
      <linearGradient id="app-store-tile" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stopColor="#18BFFB" />
        <stop offset="1" stopColor="#2072F3" />
      </linearGradient>
    </defs>
    <rect x="1" y="1" width="22" height="22" rx="5.2" fill="url(#app-store-tile)" />
    <g stroke="#fff" strokeWidth="1.75" strokeLinecap="round" fill="none">
      <path d="M13.4 5.6 8.4 14.3" />
      <path d="M7.2 16.4 6.6 17.4" />
      <path d="M10.6 5.6 16.8 16.4" />
      <path d="M5.9 14.3h12.2" />
    </g>
  </svg>
);

// Brand marks for the social links, filled, on the same 24-unit grid.
function BrandGlyph({ className, children }: IconProps & { children: React.ReactNode }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" focusable="false">
      {children}
    </svg>
  );
}

export const TikTokIcon = ({ className }: IconProps) => (
  <BrandGlyph className={className}>
    <path d="M16.6 5.82A4.28 4.28 0 0 1 15.54 3h-3.09v12.4a2.59 2.59 0 0 1-2.59 2.5c-1.42 0-2.6-1.16-2.6-2.6 0-1.72 1.66-3.01 3.37-2.48V9.66c-3.45-.46-6.47 2.22-6.47 5.64 0 3.33 2.76 5.7 5.69 5.7 3.14 0 5.69-2.55 5.69-5.7V9.01a7.35 7.35 0 0 0 4.3 1.38V7.3s-1.88.09-3.24-1.48Z" />
  </BrandGlyph>
);

export const InstagramIcon = ({ className }: IconProps) => (
  <BrandGlyph className={className}>
    <path
      fillRule="evenodd"
      d="M7.5 2.5h9a5 5 0 0 1 5 5v9a5 5 0 0 1-5 5h-9a5 5 0 0 1-5-5v-9a5 5 0 0 1 5-5Zm0 1.9a3.1 3.1 0 0 0-3.1 3.1v9a3.1 3.1 0 0 0 3.1 3.1h9a3.1 3.1 0 0 0 3.1-3.1v-9a3.1 3.1 0 0 0-3.1-3.1h-9ZM12 7.2a4.8 4.8 0 1 1 0 9.6 4.8 4.8 0 0 1 0-9.6Zm0 1.9a2.9 2.9 0 1 0 0 5.8 2.9 2.9 0 0 0 0-5.8Zm5.15-3.15a1.15 1.15 0 1 1 0 2.3 1.15 1.15 0 0 1 0-2.3Z"
    />
  </BrandGlyph>
);

export const YouTubeIcon = ({ className }: IconProps) => (
  <BrandGlyph className={className}>
    <path
      fillRule="evenodd"
      d="M23.5 6.2a3 3 0 0 0-2.1-2.1C19.5 3.6 12 3.6 12 3.6s-7.5 0-9.4.5A3 3 0 0 0 .5 6.2 31 31 0 0 0 0 12a31 31 0 0 0 .5 5.8 3 3 0 0 0 2.1 2.1c1.9.5 9.4.5 9.4.5s7.5 0 9.4-.5a3 3 0 0 0 2.1-2.1A31 31 0 0 0 24 12a31 31 0 0 0-.5-5.8ZM9.6 15.6V8.4l6.3 3.6-6.3 3.6Z"
    />
  </BrandGlyph>
);

export const XIcon = ({ className }: IconProps) => (
  <BrandGlyph className={className}>
    <path d="M18.24 2.25h3.31l-7.23 8.26 8.5 11.24h-6.65l-5.21-6.82-5.97 6.82H1.68l7.73-8.84L1.25 2.25h6.83l4.71 6.23 5.45-6.23Zm-1.16 17.52h1.83L7.08 4.13H5.12l11.96 15.64Z" />
  </BrandGlyph>
);
