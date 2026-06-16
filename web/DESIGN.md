# Lununda Agent Design System

## Brand Identity

**Lununda Agent** (org **LunaeWaves**) is a multi-user AI agent platform. The visual identity draws on lunar and deep-sea imagery: a dark horizon, moonlight reflected on moving water, and the violet glow of diffuse lunar reflection.

The brand lives in `web/src/app/globals.css` as shadcn-compatible CSS custom properties. Every component must route through the semantic tokens (`--primary`, `--accent`, `--muted`, …) — never hardcode a brand hex or an arbitrary color class from the Tailwind palette.

---

## Color Palette

### Brand Colors (source of truth)

| Token | Name | Hex | Role |
|---|---|---|---|
| `--background` (dark) | Deep Abyss Blue | `#0A1629` | Background, sidebar, dark mode base |
| `--foreground` (dark) | Pale Moon | `#F5F9FF` | Headings, body text in dark mode |
| `--primary` | Moon Cyan | `#72D1E8` | Buttons, highlights, logo accent, active states |
| `--accent` | Lunar Violet | `#9488D8` | Accent, decoratives, gradient companion, secondary emphasis |
| `--muted-foreground` | Lunar Silver | `#C9D8E8` | Body text, secondary copy, muted UI text |
| `--secondary` | Tide | Between Deep Abyss + Wave Gray (generated per theme) | Background lifts from base |
| chart / sidebar tokens | Wave Gray | `#303F58` | Dividers, charts, secondary surfaces |
| `--background` (light) | Pale Moon | `#F5F9FF` | Light mode base |

### Gradient Palettes (Logo / Banner / Hero Only)

- **Moon Wave Gradient** — `linear-gradient(135deg, #72D1E8 0%, #9488D8 100%)`
  Utility: `.bg-moon-wave`
- **Deep Sea Gradient** — `linear-gradient(180deg, #0A1629 0%, #112442 100%)`
  Utility: `.bg-deep-sea`
- **Moon Glow** — radial cyan wash for hero backdrops
  Utility: `.bg-moon-glow`

### Derivation

1. Every brand hex is converted to OKLCH once. The OKLCH values in `globals.css` are the canonical source for the CSS token layer; change the hex in this document first, then regenerate the OKLCH.

2. Dark mode uses the brand colors at or near their full saturation. Light mode darkens `--primary` and `--accent` so they stay ≥ 4.5:1 on Pale Moon (`#F5F9FF`).

3. `--secondary`, `--muted`, `--card`, `--popover`, and the sidebar equivalents are generated around the base brand colors — interpolated between Deep Abyss Blue / Wave Gray / Pale Moon rather than imported from a neutral gray palette.

---

## Semantic Token Reference

Every Tailwind utility in the codebase should route through these CSS custom properties, **never** through raw hex values or Tailwind's built-in palette (`text-violet-500`, `bg-amber-500/10`, …).

| CSS Variable | When to use |
|---|---|
| `--background` | Page body, main surface |
| `--foreground` | Primary text, headings |
| `--card` | Card backgrounds |
| `--card-foreground` | Card text |
| `--popover` | Dropdowns, tooltips, popovers |
| `--popover-foreground` | Popover text |
| `--primary` | Primary buttons, toggles, active navigation items, call-to-action surfaces |
| `--primary-foreground` | Text on primary backgrounds |
| `--secondary` | Secondary buttons, subtle backgrounds |
| `--secondary-foreground` | Text on secondary backgrounds |
| `--muted` | Muted backgrounds (badge, subtle row stripe) |
| `--muted-foreground` | Secondary text, descriptions, placeholder copy |
| `--accent` | Accent decorations, chart data, indicator dots, category badges |
| `--accent-foreground` | Text on accent backgrounds |
| `--destructive` | Delete buttons, error states, destructive confirmations |
| `--border` | Card borders, input borders, dividers |
| `--input` | Input field backgrounds |
| `--ring` | Focus rings, outline rings |
| `--sidebar` | Sidebar background |
| `--sidebar-foreground` | Sidebar text |
| `--sidebar-primary` | Active sidebar item background |
| `--sidebar-primary-foreground` | Active sidebar item text |
| `--sidebar-accent` | Hovered sidebar item background |
| `--sidebar-accent-foreground` | Hovered sidebar item text |
| `--sidebar-border` | Sidebar separators |
| `--sidebar-ring` | Sidebar focus rings |
| `--chart-1` … `--chart-5` | Chart data series (rotates through brand colors: Moon Cyan → Lunar Violet → Lunar Silver → Wave Gray → Pale Moon) |

---

## Color Rules

### 1. Icon colors — always semantic, never decorative

- **Active / primary icon** → `text-primary` on `bg-primary/10`
- **Accent icon** → `text-accent` on `bg-accent/10`
- **Success** → retain platform status color (`text-emerald-*`). Do NOT use `--primary` for success — users expect green = done.
- **Loading / in-progress** → `text-primary` (Moon Cyan), replacing the old amber-500 convention.
- **Warning** → retain platform warning color (`text-amber-*`).
- **Error / destructive** → `text-destructive` (maps to red-orange, defined in globals.css).
- **Overview stat cards** — all four cards use `--primary` for the icon ring; Chats uses `--accent` to add one deliberate accent point. Do not spread to violet/cyan/blue/amber per card.

### 2. Buttons

- **Primary button** → `bg-primary text-primary-foreground`
- **Secondary / ghost button** → `bg-secondary text-secondary-foreground` or `border border-border`
- **Accent button** → `bg-accent text-accent-foreground` (use sparingly — only for semantically distinct actions like "Chats" vs "Agents")

### 3. Status colors are exempt from the brand palette

- **Success** (emerald), **Warning** (amber), **Destructive/Error** (red-orange) are universal semantic signals. Changing them to brand colors confuses users: a delete button in Moon Cyan looks like a "confirm" button, a checkmark in Lunar Violet looks like an error.
- Keep them as-is in the Tailwind palette (standard `text-emerald-500`, `text-red-400`, etc.). They are NOT violations of the brand — they are a separate semantic layer.

### 4. Gradient usage

- `.bg-moon-wave` and `.bg-deep-sea` are for **Logo surrounds, hero banners, and loading screens only**.
- Do NOT use on body text (gradient text is banned — hard to read, inaccessible).
- Do NOT use on cards, buttons, or interactive elements (gradient backgrounds make hover/active states unpredictable).

### 5. Theme-aware logo

- **Dark theme** (`resolvedTheme === "dark"`) — render the **light logo** (`/logo-light.png`), which is a white/cyan version designed for dark backgrounds.
- **Light theme** (`resolvedTheme === "light"`) — render the **dark logo** (`/logo-dark.png`), which is a brand-color version designed for light backgrounds.
- Reference: `web/src/components/team-switcher.tsx` AgentAvatar, `web/src/app/page.tsx` RootPage.

### 6. OKLCH-first

- All brand tokens use OKLCH, not hex, in `globals.css`. The only hex literals in the codebase should be:
  - The brand gradient utilities (linear-gradient needs hex for backward compat)
  - The status colors inherited from Tailwind's palette
  - OKLCH browser support is universal as of 2024; no fallback needed.

---

## Typography

| Role | Font | Weight |
|---|---|---|
| Display / Heading | Figtree (`--font-heading`) | 400–900 |
| Body | Nunito Sans (`--font-sans`) | 200–900 |
| Mono | Geist Mono (`--font-geist-mono`) | 100–900 |

- `.font-sans` applies globally; `.font-heading` is available via the `--font-heading` variable for titles.
- Body text color is `--foreground`; secondary text is `--muted-foreground`.

---

## Icons

- **Lucide** is the primary icon set (`lucide-react`, imported per-icon).
- Channel icons live in `web/public/channels/` as SVG/PNG.
- **No icon font, no sprite sheet** — every icon is imported as a React component.

---

## Components

- UI primitives live in `web/src/components/ui/` (shadcn-generated, modified lightly).
- Feature components live in `web/src/components/` directly.
- Every component gets its color from the semantic tokens above — never from a hardcoded Tailwind color class unless it's a semantic status color (success/warning/destructive).

---

## Logo Assets

Location: `web/public/`

| File | Use | Source |
|---|---|---|
| `logo-light-*.png` | Dark-theme surfaces (sidebar header, login hero) | `images/logo-light-{256,512,1024,2048}.png` |
| `logo-dark-*.png` | Light-theme surfaces | `images/logo-dark-{256,512,1024,2048}.png` |
| `favicon.ico` | Browser tab | `images/favicon.ico` |
| `favicon-{16,32,48,64,128,180,192,256,512}.png` | `<link rel=icon>` fallbacks | `images/` — RealFaviconGenerator output |
| `apple-touch-icon.png` | iOS home screen | `images/apple-touch-icon.png` |
| `android-chrome-{192,512}.png` | PWA manifest | `images/` |
| `og-image.png` | Social sharing preview | `images/og-image.png` |
| `user.png` | Default user avatar (fallback) | Derived from favicon-180x180.png |

Updating logos: place new files in `images/`, re-run the copy steps in the build pipeline (`Makefile build-web` copies them into `web/public/`), and rebuild the binary. The PNGs are separate files, not embedded in the JS bundle.

---

## Evolution

- This document is the authority. If a brand color needs to change, **change the hex here first**, convert to OKLCH, update `globals.css`, and note the revision in git.
- When a developer asks "what color should this icon be?", point them to Rule 1 of the Color Rules section above.
- When a developer uses a Tailwind color class that isn't semantic, flag it — the fix is to replace it with a semantic token or add a note here explaining why it's exempt.
