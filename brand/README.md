# Gamics brand kit — handoff for the Flutter app

Everything a client needs to look like Gamics: the mark, app icons, colours,
type, shape language and a ready Flutter theme. The sources of truth are the
website (`app/globals.css`, `app/brand-mark.tsx`, `public/`) and the staff
dashboard (`apps/admin/src/styles/theme.css`); this folder collects them. Use
`app-icon/` below for the app icon.

## 1. The mark

A circular "G" whose counter is a game controller (four face buttons, a D-pad
cross). There is no wordmark file; set "GAMICS" in Geist Black with wide
tracking when a name is needed beside the mark.

| File | Use |
|---|---|
| `logo/gamics-mark.svg` | Master vector, full detail, `fill="currentColor"`. Recolour freely. |
| `logo/gamics-mark-solid.svg` | Silhouette cut without the buttons. Use below **48 px** — the button details are 46 of 631 units and disappear smaller. |
| `logo/gamics-mark-acid.svg` | Pre-filled acid. |
| `logo/gamics-mark-{ink,acid,paper}-1024.png` | Transparent PNG renders of the full mark. |

Rules:

- **Ink on light, acid or paper on dark.** Never acid on paper (1.16:1 — it vanishes).
- No plate, border, skew, gradient or drop shadow on the mark itself.
- Minimum size 48 px for the full mark; use the solid cut below that.
- The SVG viewBox is already cropped to the ink, so it centres correctly in
  square and circular containers.
- The source artwork is polygonal (straight segments), which shows as faint
  faceting at very large sizes. A smooth redraw is worth commissioning before launch.

For an in-app vector, add `flutter_svg` and load `gamics-mark.svg` with a
`ColorFilter` (`SvgPicture.asset(..., colorFilter: ColorFilter.mode(GamicsColors.acid, BlendMode.srcIn))`).

## 2. App icons and splash (rendered from the mark)

| File | Platform setting |
|---|---|
| `app-icon/app-icon-1024.png` | iOS + default launcher icon. Acid mark on ink, opaque (iOS rejects transparency). |
| `app-icon/app-icon-alt-ink-on-acid-1024.png` | Alternative colourway if the ink icon reads too dark on a home screen. |
| `app-icon/android-adaptive-foreground-1024.png` | Adaptive foreground; mark sits inside the 66 % safe zone. |
| `app-icon/android-adaptive-background-1024.png` | Adaptive background (flat ink `#11120F`); a colour value works too. |
| `app-icon/android-adaptive-monochrome-1024.png` | Android 13+ themed icon (white silhouette). |
| `app-icon/notification-icon-white-96.png` | Android status-bar notification icon (white solid cut). |
| `app-icon/splash-mark-acid-1152.png` | Splash image, centred on ink `#11120F`. |

With `flutter_launcher_icons`:

```yaml
flutter_launcher_icons:
  image_path: "assets/brand/app-icon-1024.png"
  android: true
  ios: true
  remove_alpha_ios: true
  adaptive_icon_background: "#11120F"
  adaptive_icon_foreground: "assets/brand/android-adaptive-foreground-1024.png"
  adaptive_icon_monochrome: "assets/brand/android-adaptive-monochrome-1024.png"
```

With `flutter_native_splash`:

```yaml
flutter_native_splash:
  color: "#11120F"
  image: "assets/brand/splash-mark-acid-1152.png"
  android_12:
    color: "#11120F"
    image: "assets/brand/splash-mark-acid-1152.png"
```

App identity carried over from the earlier app: name **Gamics**, bundle/package ID
**`io.gamics.app`**, deep-link scheme **`gamics://`**, portrait only, dark UI.

## 3. Colour

The app is **dark**: ink background, panel surfaces, paper text, acid as the one
loud accent. The website is the light inverse (paper background, ink text).

| Token | Hex | Role |
|---|---|---|
| `ink` | `#11120F` | App background; text on acid and light surfaces |
| `panel` | `#1B1D19` | Cards, sheets, tab bar on dark |
| `paper` | `#F4F1E8` | Primary text on dark; light-mode background |
| `acid` | `#C7F135` | Brand accent: primary buttons, active tab, highlights, wins, live state |
| `blue` | `#5677FF` | Secondary accent, info, "home" side |
| `orange` | `#FF7448` | Losses, alerts, destructive, "away" side |
| `green` | `#4CCB74` | Success, verified |
| `muted` | `#92958D` | Secondary text |
| `subtleInk` | `#5D6058` | Disabled and decorative only |
| line on dark | `rgba(244,241,232,0.15)` | Dividers and 1 px borders on ink |
| line on light | `rgba(17,18,15,0.18)` | Dividers on paper |

Measured contrast (WCAG): acid on ink 14.4, paper on ink 16.6, muted on ink 6.2,
muted on panel 5.6, green on ink 9.0, orange on ink 7.0, blue on ink 4.9 (large
or bold text only), white on blue 3.8 (large display numerals only), subtleInk on
ink 2.9 (never for readable text), acid on paper 1.2 (never).

## 4. Typography

- **Geist** for everything, **Geist Mono** for labels, stats, timestamps, tags
  and anything tabular. Both are OFL; use the `google_fonts` package or bundle
  the files from the Vercel `geist-font` release.
- **Display**: Geist weight 900, very tight — letter-spacing about −7 % of the
  font size, line height 0.85. Big numbers and headlines are the brand's voice.
- **Labels / eyebrows**: Geist Mono 700, UPPERCASE, letter-spacing about +10 %.
- **Buttons**: Geist 800, UPPERCASE, letter-spacing about +4 %.
- **Body**: Geist 400–500, line height 1.5.
- **Outline type** (stroked, no fill) is used for one emphasised word in a headline.

## 5. Shape and motion language

Street-poster / scoreboard energy, not soft and glossy:

- 1 px solid borders (ink on light, line colour on dark).
- **Hard offset shadows with no blur**: primary buttons get a 5 × 5 ink shadow;
  hero cards get an 11 × 11 acid shadow.
- Square or lightly rounded corners. The website is fully square; the app used
  radii 8 / 13 / 18 — keep to those, never pill-round cards.
- Slight rotation (about 2°) on feature cards, small rotated squares and circles
  in orange and blue as accent "signals".
- Acid ticker bands with mono uppercase text.
- Spacing scale: 4, 8, 16, 22, 32, 56.
- Form chips: W on acid, L on orange, square, mono.

`marketing/social-card-1731x909.png` shows the full art direction: halftone
photography, acid/blue/orange slabs, condensed headline type, scoreboard and
bracket graphics. Its "Kenya" line predates the India launch; keep the style,
not the copy. Tagline: **"Your game. Your name."**

## 6. Flutter theme

```dart
import 'package:flutter/material.dart';
import 'package:google_fonts/google_fonts.dart';

abstract final class GamicsColors {
  static const ink = Color(0xFF11120F);
  static const panel = Color(0xFF1B1D19);
  static const paper = Color(0xFFF4F1E8);
  static const acid = Color(0xFFC7F135);
  static const blue = Color(0xFF5677FF);
  static const orange = Color(0xFFFF7448);
  static const green = Color(0xFF4CCB74);
  static const muted = Color(0xFF92958D);
  static const subtleInk = Color(0xFF5D6058);
  static const line = Color(0x26F4F1E8); // paper at 15 %
}

abstract final class GamicsSpace {
  static const xs = 4.0, sm = 8.0, md = 16.0, lg = 22.0, xl = 32.0, xxl = 56.0;
}

abstract final class GamicsRadius {
  static const sm = 8.0, md = 13.0, lg = 18.0;
}

ThemeData gamicsTheme() {
  final base = ThemeData(brightness: Brightness.dark, useMaterial3: true);
  final text = GoogleFonts.geistTextTheme(base.textTheme).apply(
    bodyColor: GamicsColors.paper,
    displayColor: GamicsColors.paper,
  );
  return base.copyWith(
    scaffoldBackgroundColor: GamicsColors.ink,
    colorScheme: const ColorScheme.dark(
      primary: GamicsColors.acid,
      onPrimary: GamicsColors.ink,
      secondary: GamicsColors.blue,
      onSecondary: GamicsColors.ink,
      tertiary: GamicsColors.orange,
      error: GamicsColors.orange,
      onError: GamicsColors.ink,
      surface: GamicsColors.panel,
      onSurface: GamicsColors.paper,
      onSurfaceVariant: GamicsColors.muted,
      outline: GamicsColors.line,
    ),
    textTheme: text.copyWith(
      displayLarge: text.displayLarge?.copyWith(fontWeight: FontWeight.w900, letterSpacing: -4, height: 0.85),
      headlineMedium: text.headlineMedium?.copyWith(fontWeight: FontWeight.w900, letterSpacing: -1),
      labelSmall: GoogleFonts.geistMono(fontWeight: FontWeight.w700, letterSpacing: 1, color: GamicsColors.muted),
    ),
    dividerColor: GamicsColors.line,
    cardTheme: CardThemeData(
      color: GamicsColors.panel,
      elevation: 0,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(GamicsRadius.md),
        side: const BorderSide(color: GamicsColors.line),
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        backgroundColor: GamicsColors.acid,
        foregroundColor: GamicsColors.ink,
        minimumSize: const Size.fromHeight(48),
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(GamicsRadius.sm)),
        textStyle: GoogleFonts.geist(fontWeight: FontWeight.w800, letterSpacing: 0.6),
      ),
    ),
    navigationBarTheme: const NavigationBarThemeData(
      backgroundColor: GamicsColors.panel,
      indicatorColor: GamicsColors.acid,
    ),
  );
}

/// The brand's hard, blurless offset shadow.
const gamicsHardShadow = [BoxShadow(color: GamicsColors.ink, offset: Offset(5, 5))];
const gamicsCardShadow = [BoxShadow(color: GamicsColors.acid, offset: Offset(11, 11))];
```

Machine-readable values are in `tokens.json`.
