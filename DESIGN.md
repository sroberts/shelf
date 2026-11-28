---
name: shelf
description: A personal e-book library and e-reader sync tool, shown the way a good store shows its products.
colors:
  ground: "#ffffff"
  tile: "#f5f5f7"
  tile-pressed: "#e8e8ed"
  ink: "#1d1d1f"
  ink-secondary: "#424245"
  muted: "#6e6e73"
  hairline: "#d2d2d7"
  field-border: "#86868b"
  graphite: "#1d1d1f"
  graphite-hover: "#000000"
  graphite-ink: "#ffffff"
  danger: "#c62a1e"
  danger-wash: "#fdeceb"
  caution: "#8a5300"
  caution-wash: "#fdf3e3"
  night-ground: "#000000"
  night-tile: "#1c1c1e"
  night-tile-pressed: "#2c2c2e"
  night-ink: "#f5f5f7"
  night-ink-secondary: "#d1d1d6"
  night-muted: "#98989d"
  night-hairline: "#38383a"
  night-field-border: "#7c7c80"
  night-graphite: "#f5f5f7"
  night-graphite-ink: "#000000"
  night-danger: "#ff7066"
  night-danger-wash: "#3a1714"
  night-caution: "#f0a83a"
  night-caution-wash: "#33240c"
typography:
  display:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "clamp(1.75rem, 1.35rem + 1.6vw, 2.5rem)"
    fontWeight: 650
    lineHeight: 1.15
    letterSpacing: "-0.025em"
  title:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1.25rem"
    fontWeight: 650
    lineHeight: 1.15
    letterSpacing: "-0.015em"
  heading-sm:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1rem"
    fontWeight: 650
    lineHeight: 1.15
    letterSpacing: "-0.01em"
  lede:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1.0625rem"
    fontWeight: 400
    lineHeight: 1.5
  body:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.5
    letterSpacing: "-0.005em"
  book-title:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "-0.01em"
  meta:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.35
  label:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "0.875rem"
    fontWeight: 550
    lineHeight: 1.35
  small:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.45
  mono:
    fontFamily: "ui-monospace, 'SF Mono', Menlo, Consolas, monospace"
    fontSize: "0.88em"
    fontWeight: 400
rounded:
  cover: "3px"
  sm: "8px"
  field: "10px"
  md: "12px"
  tile: "18px"
  pill: "999px"
spacing:
  xs: "0.5rem"
  sm: "0.75rem"
  md: "1rem"
  lg: "1.5rem"
  xl: "2rem"
  xxl: "2.5rem"
  page-max: "76rem"
  bar: "3.25rem"
  tabs: "3.75rem"
components:
  button-primary:
    backgroundColor: "{colors.graphite}"
    textColor: "{colors.graphite-ink}"
    typography: "{typography.label}"
    rounded: "{rounded.pill}"
    padding: "0.4rem 0.95rem"
    height: "2.25rem"
  button-primary-hover:
    backgroundColor: "{colors.graphite-hover}"
  button-secondary:
    backgroundColor: "{colors.tile-pressed}"
    textColor: "{colors.ink}"
    typography: "{typography.label}"
    rounded: "{rounded.pill}"
    padding: "0.4rem 0.95rem"
    height: "2.25rem"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.ink}"
    rounded: "{rounded.pill}"
    padding: "0.4rem 0.95rem"
    height: "2.25rem"
  button-ghost-hover:
    backgroundColor: "{colors.tile}"
  button-danger:
    backgroundColor: "{colors.danger-wash}"
    textColor: "{colors.danger}"
    rounded: "{rounded.pill}"
    padding: "0.4rem 0.95rem"
    height: "2.25rem"
  button-small:
    rounded: "{rounded.pill}"
    padding: "0.2rem 0.7rem"
    height: "1.875rem"
  filter-pill:
    backgroundColor: "{colors.tile}"
    textColor: "{colors.ink}"
    typography: "{typography.label}"
    rounded: "{rounded.pill}"
    padding: "0.25rem 1.9rem 0.25rem 0.85rem"
    height: "2rem"
  filter-pill-set:
    backgroundColor: "{colors.graphite}"
    textColor: "{colors.graphite-ink}"
  input-field:
    backgroundColor: "{colors.ground}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.field}"
    padding: "0.45rem 0.75rem"
    height: "2.5rem"
  search-field:
    backgroundColor: "{colors.tile}"
    textColor: "{colors.ink}"
    typography: "{typography.meta}"
    rounded: "{rounded.pill}"
    height: "2rem"
    width: "15rem"
  nav-item:
    textColor: "{colors.muted}"
    typography: "{typography.label}"
    rounded: "{rounded.pill}"
    padding: "0.3rem 0.75rem"
  nav-item-active:
    backgroundColor: "{colors.tile-pressed}"
    textColor: "{colors.ink}"
  cover-tile:
    backgroundColor: "{colors.tile}"
    rounded: "{rounded.tile}"
    padding: "13% 21% 11%"
  cover:
    rounded: "{rounded.cover}"
  panel:
    backgroundColor: "{colors.tile}"
    rounded: "{rounded.tile}"
    padding: "1.5rem"
  notice:
    backgroundColor: "{colors.tile}"
    textColor: "{colors.ink-secondary}"
    typography: "{typography.meta}"
    rounded: "{rounded.md}"
    padding: "0.75rem 1rem"
  chip:
    backgroundColor: "{colors.tile}"
    textColor: "{colors.ink}"
    typography: "{typography.label}"
    rounded: "{rounded.pill}"
    padding: "0.2rem 0.25rem 0.2rem 0.85rem"
  toast:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.ground}"
    typography: "{typography.meta}"
    rounded: "{rounded.md}"
    padding: "0.6rem 0.6rem 0.6rem 1rem"
  dialog:
    backgroundColor: "{colors.ground}"
    textColor: "{colors.ink}"
    rounded: "{rounded.tile}"
    width: "min(32rem, calc(100vw - 2rem))"
  progress-bar:
    backgroundColor: "{colors.hairline}"
    rounded: "2px"
    height: "3px"
---

# Design System: shelf

## Overview

**Creative North Star: "The Showroom"**

shelf's web UI presents books the way an Apple Store presents products. The grounds are pure white by day and true black by night. Neutral greys carry no warm or cool cast. One system typeface runs throughout, and the chrome is quiet enough to read as absent. The books supply the colour. Every cover stands on its own soft grey plinth, lifted off it by a real contact shadow, and nothing else on the screen competes with it. The craft bar is Linear's: a strict type scale, tabular numerals wherever figures line up, and controls finished at every state (hover, focus, pressed, disabled, coarse pointer).

This replaces the earlier warm, bookish world of cream paper, Literata headings, bookcloth green and gilt progress. Its plain, consequence-aware copy and its flat-with-lifted-covers depth carry over. Its materials do not.

The screens are working tools first. Navigation sits in a slim top bar, filters are a row of pills, forms are labelled fields and details are a hairline-ruled table. The world reaches the screen through type, a neutral palette, air, and one signature move: the cover on its plinth, used wherever a book appears.

**Key Characteristics:**
- White and true-black grounds, with a neutral grey tile one step off each.
- Book covers are the only saturated colour; interface colour appears only for danger and caution.
- Graphite (near-black, inverted to near-white at night) is the single accent: primary buttons, set filters, focus rings, progress fills and the toast.
- One system sans-serif at a few sizes, with hierarchy carried by weights 400, 550, 600 and 650.
- Flat surfaces separated by tone and space; only covers and overlays cast shadows.
- Pill-shaped controls, 18px tiles, near-square 3px book corners.

## Colors

A neutral system in which interface colour is rationed: the books bring the colour, and the interface brings graphite. Every token has a night counterpart (the `night-*` keys), swapped wholesale under `prefers-color-scheme: dark`.

### Primary
- **Graphite** (`graphite`, night `night-graphite`): the only accent. Primary buttons, a set filter pill, the selected view, focus outlines, progress fills and checkbox accents. Hover goes one step further toward pure black (pure white at night). Graphite text sits on Graphite Ink.

### Neutral
- **Showroom White** (`ground`) / **Night** (`night-ground`): the page, the dialog, and a focused search field.
- **Plinth Grey** (`tile`) / **Night Tile** (`night-tile`): the plinth a cover stands on, panels, shelf cards, notices, chips, filter pills and the resting search field.
- **Pressed Grey** (`tile-pressed`): secondary buttons, the active nav item, inline code, and the hover state of tile-coloured controls.
- **Ink** (`ink`): primary text and links. **Secondary Ink** (`ink-secondary`): notice text and the clear-search glyph. **Muted** (`muted`): authors, counts, helper text, placeholders and inactive nav.
- **Hairline** (`hairline`): the bar's bottom rule, list-row and details-table dividers, and the progress track.
- **Field Edge** (`field-border`): the 1px stroke on text inputs and selects, 3.3:1 on tile and 3.6:1 on ground.

### Status (meaning only)
- **Danger** (`danger` on `danger-wash`): the danger button, error notices, the error toast, invalid-field strokes and destructive text actions.
- **Caution** (`caution`, `caution-wash`): warning notices, which keep ink text on the wash.

### Named Rules
**The Covers Are the Colour Rule.** No interface element is saturated except where colour carries meaning (danger, caution). Coverless books get a generated stand-in (one of sixteen deep, muted cloth hues at 30% saturation and 21–30% lightness, with near-white type) so that they still read as books. If a screen without covers looks colourful, something is wrong.

**The One Accent Rule.** Graphite is the only accent. There is no brand green, no link blue and no gilt. Links are ink with a 1px underline that thickens to 2px on hover.

**The Muted Stays Off Pressed Rule.** Muted text falls to 4.2:1 on Pressed Grey. It goes on the ground or the plinth, never on a pressed fill.

## Typography

**Body and Display Font:** the system sans-serif stack (`system-ui`, `-apple-system`, so SF Pro on Apple devices, with Segoe UI, Roboto and Helvetica Neue as fallbacks).
**Label/Mono Font:** the system monospace (`ui-monospace`, SF Mono), only for real paths, addresses, ids and keyboard hints.

**Character:** one familiar typeface carries everything through size and weight. It sets an Apple Store calm in page titles and Linear-grade precision in dense lists.

### Hierarchy
- **Display** (650, 28–40px fluid, 1.15, -0.025em): page titles (`h1`), balanced wrapping.
- **Title** (650, 20px, 1.15, -0.015em): section headings, the author line on a book page, dialog and drop-zone titles.
- **Heading Small** (650, 16px): sub-section heads such as Reading, Shelves and Details on a book page.
- **Lede** (400, 17px): the introductory paragraph on Shelves, Reading and a book's description, capped at 38–44rem.
- **Body** (400, 16px, 1.5, -0.005em): default text. Fields are also 16px, which keeps iOS from zooming on focus.
- **Book Title** (600, 15px, 1.3): the title under a cover, clamped to two lines; 550 weight in list rows.
- **Meta** (400, 14px, Muted): authors, counts, helper text and table labels.
- **Label** (550, 14px): buttons, nav, filter pills, field labels and chips. Sentence case.
- **Small** (13px): small buttons, the format tag, percent labels and version lines.

### Named Rules
**The No Eyebrow Rule.** No small uppercase kicker above headings, and no tracked uppercase labels. Hierarchy comes from size and weight alone. The format tag (EPUB) is uppercase because it is the format's name, not a label style.

**The One Family Rule.** No serif, and no second display face. The covers carry the typographic personality, so the interface does not compete.

**The Tabular Figures Rule.** Counts, percentages, sizes and years use `tabular-nums` so that columns and progress figures stay aligned.

## Layout

A centred column at most 76rem wide, with 1.5rem side padding (1rem on a phone, plus safe-area insets), 2.5rem above the content and 5rem below. The rhythm is rem-based and steps through 0.5, 0.75, 1, 1.5, 2 and 2.5rem for structure, with tighter steps (0.15–0.6rem) inside components.

- **Top bar:** sticky and 3.25rem tall, holding the lowercase wordmark, pill nav, a 15rem search that widens to 19rem on focus (`/` focuses it), and an About icon.
- **Page head:** the Display title with the one primary action aligned right, the count in Muted 1.25rem under it, then the toolbar: filter pills left, search tips, sort and grid/list toggle right.
- **Cover grid:** `auto-fill` columns of at least 12.5rem, with 2.5rem row gaps and 1.5rem column gaps. Each 5:6 plinth holds a 2:3 cover. At 560px and below the grid becomes two columns with 1.75rem / 0.75rem gaps.
- **List view:** the dense alternative. Each row has a 2.25rem thumbnail, the title and author, and fixed right-aligned meta columns. The year and size columns drop below 600px.
- **Book page:** a cover column of 16–26rem beside the info column, with a 4rem gap, stacking below about 720px.
- **Phone (720px and below):** top nav and bar search hide. A fixed, opaque bottom tab bar (3.75rem, four icon-over-label tabs) takes over, the page gets its own search box, and filters become one sideways-scrolling row that fades at its right edge.
- **Coarse pointers:** buttons, fields and filter pills grow to 2.75rem (2.5rem for small buttons), and the grid/list toggle grows to 2.75 by 2.5rem.

## Elevation & Depth

Flat by default. Pages, panels, shelf cards and notices differ in tone (ground versus plinth), never by shadow. Two things lift. Book covers sit on their plinth with a realistic contact shadow, and at night they also get a faint light rim so the lift still reads on a dark plinth. Overlays (dialogs, toasts and the drop card) float with a diffuse ambient shadow over a scrim. The sticky top bar uses a 94% ground with a saturate-and-blur backdrop and a hairline drawn as a 1px shadow. That is Apple's frosted bar, used for legibility rather than decoration. Without backdrop-filter support it falls back to an opaque ground. The phone tab bar is opaque because covers scroll beneath it.

### Shadow Vocabulary
- **Lift** (`--lift`): every cover at rest.
- **Lift Hover** (`--lift-hover`): a cover whose card is hovered, together with a 4px rise.
- **Float** (`--float`): dialogs, toasts and the drag-and-drop card. At night it becomes a 1px hairline ring plus a deep shadow.

### Named Rules
**The Lifted Books Rule.** Only covers and overlays cast shadows. A panel, button or card with a shadow is a mistake.

**The Spine Rule.** Every cover carries a 6%-wide fold gradient on its left edge, so a flat image reads as a bound book.

## Shapes

Three radii carry the system. Tiles, panels, dialogs and shelf cards use a generous **18px**, like Apple's product tiles. Notices and toasts use **12px**, and text fields **10px**. Every interactive control that is not a text field is a **pill**: buttons, nav items, filter pills, the segmented toggle, chips, badges and the bar search. Covers keep a near-square **3px** corner so they read as printed books rather than UI cards. Progress tracks are 3px tall with 2px ends. Icon discs and clear buttons are circles.

## Components

### Buttons
Quiet, exact pills.
- **Shape:** full pill (999px), 2.25rem tall, label weight 550 at 14px, with an optional 0.45rem-gapped leading icon.
- **Primary:** Graphite with Graphite Ink. Use one per area (Add books, Download, Save).
- **Secondary:** Pressed Grey with ink. Hover mixes 18% ink into the fill.
- **Ghost:** transparent, Plinth Grey on hover. Used for icon buttons and toast dismissals.
- **Danger:** danger text on the danger wash.
- **States:** pressing scales to 0.98. Disabled drops to 45% opacity. Focus is a 2px Graphite outline at 2px offset. Variants include `small` (1.875rem) and `icon` (square, 2.25rem).

### Filter pills and segmented toggle
- **Filter pill:** a Plinth Grey pill holding a native select that is only as wide as its chosen value (`field-sizing: content`), with a drawn chevron. Once set it turns Graphite: the accent marks what narrows the list.
- **Segmented toggle:** a Plinth Grey pill track with a 2px inset. The pressed segment becomes a ground-coloured thumb with ink icon.

### Cards / Containers
- **Panel:** Plinth Grey, 18px radius, 1.5rem padding, no border and no shadow. Examples are the reading card and a device card.
- **Shelf card:** Plinth Grey row with an icon disc in ground, name, and a count. Hover deepens to Pressed Grey.
- **Notice:** 12px radius, 14px text. Neutral on plinth, caution wash with ink, or danger wash with danger text.

### Inputs / Fields
- **Style:** ground fill, 1px Field Edge stroke, 10px radius, 2.5rem tall, 16px text. Selects draw their own ink chevron.
- **Label:** 14px weight 550 in ink above the field. Helper text is 13px Muted.
- **Focus:** a 2px Graphite outline replaces the stroke.
- **Error:** the stroke turns Danger and a 14px weight-550 message follows.
- **Search boxes:** the bar search and the phone's page search sit on the plinth with no stroke. On focus they switch to the ground with a 2px Graphite inset outline.

### Navigation
- **Desktop:** lowercase **shelf** wordmark (700, 17px, -0.03em) with its drawn mark, then nav items in Muted 14px/550 pills. Hover goes to ink. The current page gets ink on Pressed Grey.
- **Phone:** bottom tab bar with 22px Lucide line icons over 11px labels, Muted at rest and ink when current.

### Toasts and dialogs
- **Toast:** an ink rectangle (12px radius) with ground text and Float, bottom-centred above the tab bar. Errors use the danger fill.
- **Dialog:** ground, 18px radius, Float, a 32% black scrim (60% at night) with a 2px blur. It rises 8px from 98% scale over 0.28s on the shared ease-out curve.

### The Cover on its Plinth (signature)
Each book is a link holding a 5:6 Plinth Grey tile (18px radius). The 2:3 cover is anchored at the bottom centre, with 13% / 21% / 11% padding. Below sit the book title (600, 15px, two lines) and a row with the author in Muted and, when the book is in progress, the percent in 13px/600 ink. An in-progress book shows a 3px Graphite progress bar on the plinth's lower edge, under the cover, like a bookmark. On hover the plinth deepens 30% toward Pressed Grey, the cover rises 4px over 0.35s, and its shadow moves to Lift Hover. Keyboard focus draws the 2px outline around the plinth. The same plinth carries the large cover on a book page. A coverless book gets a typeset stand-in: title (700), a short rule and the author, on a hue picked from the title, in one of three arrangements (foot, head, band).

### Motion
One curve, `cubic-bezier(0.16, 1, 0.3, 1)`, for everything. Colour changes take 0.15s, cover lifts 0.35s and overlays 0.28–0.3s. `prefers-reduced-motion` disables all transitions and animations.

## Do's and Don'ts

### Do:
- **Do** put every book on a Plinth Grey tile with the Lift shadow and the spine fold; let covers be the largest and most colourful thing on the screen.
- **Do** use Graphite for the one primary action in an area, and Pressed Grey or ghost pills for the rest.
- **Do** separate regions with space and the plinth tone before reaching for a hairline; keep hairlines for list rows, detail tables and the bar edge.
- **Do** keep both modes at WCAG 2.2 AA, including 3:1 for field edges, and keep Muted text off Pressed Grey.
- **Do** grow every control to 2.75rem under a coarse pointer.
- **Do** write the product name in lowercase: **shelf**.

### Don't:
- **Don't** reintroduce cream or paper grounds, serif headings, bookcloth green or gilt; that world is retired.
- **Don't** add a brand colour, gradient, glow or decorative glass; translucency appears only in the frosted top bar and the drag-and-drop veil, both there for legibility.
- **Don't** put shadows on panels, cards or buttons.
- **Don't** use uppercase tracked labels or kickers above headings.
- **Don't** let a working screen turn into a showcase: filters, forms and tables stay standard controls in familiar places.
