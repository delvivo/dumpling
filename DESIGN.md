---
name: Dumpling
description: Self-hosted hot backup and restore for PostgreSQL, MySQL and SQL Server.
colors:
  rice-paper: "#fafaf9"
  soy-ink: "#1c1917"
  steam-grey: "#78716c"
  basket-line: "#e7e5e4"
  wrapper-white: "#ffffff"
  fried-wrapper-amber: "#b45309"
  chili-red: "#b91c1c"
  scallion-green: "#15803d"
  mustard: "#a16207"
  chili-red-night: "#f87171"
  scallion-green-night: "#4ade80"
  mustard-night: "#facc15"
  night-steam: "#f5f5f4"
  night-grey: "#a8a29e"
  night-line: "#44403c"
  control-line: "#8c847d"
  control-line-night: "#857d76"
  night-tray: "#292524"
  fried-wrapper-amber-night: "#f59e0b"
  terminal-black: "#0c0a09"
typography:
  headline:
    fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "1.4rem"
    fontWeight: 700
  body:
    fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "15px"
    fontWeight: 400
  label:
    fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "0.9rem"
    fontWeight: 400
  table-header:
    fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "0.85rem"
    fontWeight: 600
  status:
    fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif"
    fontSize: "0.8rem"
    fontWeight: 400
  log:
    fontFamily: "ui-monospace, monospace"
    fontSize: "0.85rem"
    fontWeight: 400
rounded:
  control: "6px"
  tray: "8px"
  dialog: "10px"
  pill: "999px"
spacing:
  xs: "0.25rem"
  sm: "0.4rem"
  md: "0.75rem"
  lg: "1rem"
  xl: "1.5rem"
components:
  button:
    backgroundColor: "{colors.wrapper-white}"
    textColor: "{colors.soy-ink}"
    rounded: "{rounded.control}"
    padding: "0.4rem 0.8rem"
  button-primary:
    backgroundColor: "{colors.fried-wrapper-amber}"
    textColor: "{colors.wrapper-white}"
    rounded: "{rounded.control}"
    padding: "0.4rem 0.8rem"
  button-danger:
    backgroundColor: "{colors.chili-red}"
    textColor: "{colors.wrapper-white}"
    rounded: "{rounded.control}"
    padding: "0.4rem 0.8rem"
  button-link:
    textColor: "{colors.fried-wrapper-amber}"
    padding: "0"
  input:
    backgroundColor: "{colors.rice-paper}"
    textColor: "{colors.soy-ink}"
    rounded: "{rounded.control}"
    padding: "0.4rem 0.5rem"
  card:
    backgroundColor: "{colors.wrapper-white}"
    rounded: "{rounded.tray}"
    padding: "1rem"
  nav:
    backgroundColor: "{colors.wrapper-white}"
    textColor: "{colors.soy-ink}"
    padding: "0.75rem 1.5rem"
  status-pill:
    typography: "{typography.status}"
    rounded: "{rounded.pill}"
    padding: "0.1rem 0.45rem"
  log:
    backgroundColor: "{colors.terminal-black}"
    textColor: "{colors.basket-line}"
    typography: "{typography.log}"
    rounded: "{rounded.tray}"
    padding: "1rem"
---

# Design System: Dumpling

## Overview

**Creative North Star: "The Steamer Basket"**

Dumpling is a stack of plain trays. Every surface is a flat white tray on warm rice paper, edged by a thin stone line, holding exactly what it says it holds: a table of backups, a form, a log. Nothing is hidden under decoration, and nothing floats. The one warm note is the amber of a pan-fried wrapper, reserved for the action you came to take and for where you are.

The system is deliberately small: one stylesheet (`web/src/styles.css`), nine color tokens, the platform's own font, and a handful of utility classes. Density is that of an operator's tool, compact tables and forms at a 15px base, readable on a weekly check-in and calm during a restore. Status lives in color-coded outline pills; failures are red and plain, never softened.

Dark mode follows the OS (`prefers-color-scheme`) and flips the trays to warm charcoal while the amber brightens. The live operation log is always a dark terminal, in both themes.

**Key Characteristics:**
- Flat trays on warm paper; 1px stone borders carry all structure.
- Single accent (amber) for primary action, active nav, links and running states.
- System font stack only; no web fonts.
- Outline status pills color-coded by outcome.
- Native elements styled lightly (`<dialog>`, `<table>`, form controls), not replaced.

## Colors

Warm stone neutrals with one amber accent and three semantic outcome colors.

### Primary
- **Fried-Wrapper Amber** (light `fried-wrapper-amber`, dark `fried-wrapper-amber-night`): primary buttons, active nav item (text plus 2px underline), link buttons, and running/pending status pills.

### Neutral
- **Rice Paper** (`rice-paper`): page background; also the fill of inputs, so fields read as recessed into the tray.
- **Wrapper White** (`wrapper-white`): trays (cards, forms, tables, nav bar, dialogs) and default buttons.
- **Soy Ink** (`soy-ink`): body text. In dark mode it becomes the page background.
- **Steam Grey** (`steam-grey`): secondary text, table headers, the signed-in user.
- **Basket Line** (`basket-line`): tray, table, nav and dialog borders and row dividers. Also the log text color.
- **Control Line** (`control-line`, `control-line-night` in dark mode): borders of buttons and fields, ≥3:1 against their surroundings so controls read as controls.
- **Night tray set** (`night-tray`, `night-line`, `night-grey`, `night-steam`): the dark-mode equivalents of tray, border, muted and text.
- **Terminal Black** (`terminal-black`): the operation log background in both themes.

### Semantic
- **Chili Red** (`chili-red`): danger buttons, error text, failed/corrupted/interrupted status.
- **Scallion Green** (`scallion-green`): success/ok status.
- **Mustard** (`mustard`): warnings and skipped status; warning banners use it at 10% as a tint (`color-mix`).

In dark mode each semantic color brightens (`chili-red-night`, `scallion-green-night`, `mustard-night`) to stay above 4.5:1 on the night trays. Text on filled buttons follows `--on-fill`: white in light mode, Soy Ink in dark mode. The root declares `color-scheme: light dark` so native controls follow the theme.

### Named Rules
**The One Warm Note Rule.** Amber is the only accent. It marks the primary action, the current location and work in progress; it never decorates.

**The Outcome Color Rule.** Red, green and mustard mean failed, succeeded and needs attention. Never use them for anything else.

## Typography

**Body Font:** system-ui (with -apple-system, Segoe UI, sans-serif)
**Label/Mono Font:** browser default monospace for `pre` and `code`

**Character:** Platform-native and unbranded; the tool feels like part of the operator's machine.

### Hierarchy
- **Headline** (700, 1.4rem): one page title per screen; it is a flex row that can hold actions next to the title.
- **Body** (400, 15px): table cells, form values, prose.
- **Label** (400, 0.9rem): form field labels, stacked above their control.
- **Table header** (600, 0.85rem, Steam Grey): column headings.
- **Status** (400, 0.8rem): status pill text.
- **Log** (monospace, 0.85rem): operation logs; `code` runs at 0.85em inline.

### Named Rules
**The Native Voice Rule.** No web fonts. Hierarchy comes from size, weight and Steam Grey, not from a second family.

## Layout

A top nav bar spans the full width; content sits in a centered `main` capped at 1200px with 1.5rem padding. Pages are vertical stacks: headline row, optional filters row, then a table or tray. Forms use an auto-fit grid (`minmax(160px, 1fr)`) so fields reflow to fewer columns on narrow screens. Nav, action groups, filters and the headline row use wrapping flex rows.

One breakpoint, **narrow (≤640px)**: page and nav padding drop to 1rem, and list tables stack into trays, one tray per row with a "label: value" line per cell (label from the cell's `data-label`, in Steam Grey at 0.85rem, 6.5rem column) and the row actions underneath. Every data cell of a list table carries `data-label`. On touch devices (`pointer: coarse`) buttons and fields are at least 44px tall and fields use 16px text so iOS doesn't zoom on focus. Login and setup trays are fluid (`min(340–360px, 100%)`).

Spacing steps observed: 0.25, 0.4, 0.5, 0.75, 1 and 1.5rem. Trays pad 1rem with 0.75rem gaps; action groups gap 0.4rem.

## Elevation & Depth

Flat. There are no shadows anywhere. Depth comes from tone (white tray on rice paper, rice-paper inputs inside white trays) and 1px Basket Line borders. The only overlay is the native `<dialog>`, whose backdrop dims the page with 40% black.

### Named Rules
**The No-Lift Rule.** Trays sit flat. If something needs to stand apart, give it a border or a tonal step, never a shadow.

## Shapes

Gently rounded and consistent by size: controls (buttons, inputs, warning banners) 6px, trays and the log 8px, dialogs 10px, status pills fully round. Every container has a 1px border except the log, which is set apart by its dark fill.

## Components

Restrained: plain bordered controls, with color reserved for intent.

### Buttons
- **Shape:** gently rounded (6px), 1px Control Line border, inherits body font.
- **Default:** Wrapper White fill, Soy Ink text. Row actions (Restore, Verify, Edit…) are always default buttons; amber belongs only to the page or dialog primary action.
- **Subtle danger:** default button with Chili Red text; for row-level Delete (the confirmation that follows carries the weight).
- **Primary:** Fried-Wrapper Amber fill and border, `--on-fill` text (white light, Soy Ink dark). One per form or dialog.
- **Danger:** Chili Red fill and border, `--on-fill` text; for destructive confirmation.
- **Link:** borderless, amber text, no padding; used for inline actions like Log out.
- **Disabled:** 50% opacity, `not-allowed` cursor.
- **Hover** (pointer devices only): default buttons darken their border to Soy Ink; filled buttons shift brightness (`--fill-hover`: 0.9 light, 1.1 dark); links and nav items turn amber with an underline.
- **Focus:** every focusable element shows a 2px amber `:focus-visible` outline, 2px offset (1px on fields).

### Status Pills
- **Style:** outline only (1px currentColor border, no fill), fully rounded, 0.8rem, nowrap.
- **State:** green for success/ok, mustard for warning/skipped, red for failed/corrupted/interrupted, amber for running/pending.

### Cards / Containers (Trays)
- **Corner Style:** 8px.
- **Background:** Wrapper White (Night Tray in dark mode).
- **Shadow Strategy:** none (see Elevation & Depth).
- **Border:** 1px Basket Line.
- **Internal Padding:** 1rem, children in a grid with 0.75rem gap. Forms share this exact treatment.

### Inputs / Fields
- **Style:** 1px Control Line border, 6px radius, Rice Paper fill, inherits body font.
- **Labels:** stacked above with a 0.25rem gap at 0.9rem; checkboxes use an inline label (control and text side by side).
- **Focus:** 2px amber outline, 1px offset.
- **Error:** Chili Red text below the field or form (`.error`).

### Navigation
- Horizontal bar on Wrapper White with a bottom border. Brand (🥟 Dumpling, bold) left, links in Soy Ink with no underline, user name and Log out pushed right. Active link turns amber with a 2px amber underline. Wraps on narrow screens.

### Tables
- Full width, Wrapper White, collapsed borders; each row separated by a 1px Basket Line; cells pad 0.5rem 0.6rem and top-align.

### Warning Banner
- Mustard text on a 10% mustard tint, 6px radius; used for engine limitations and insecure-bind notices.

### Operation Log (signature)
- Terminal Black panel with Basket Line text, 8px radius, 1rem padding, monospace 0.85rem, scrolls within 60vh. Stays dark in both themes, so a log always reads as machine output.

## Do's and Don'ts

### Do:
- **Do** keep everything in `web/src/styles.css` with tokens as `:root` custom properties; add a dark value in the `prefers-color-scheme` block for any new neutral.
- **Do** build screens from the existing trays, tables, stacked labels and `.row` grid before inventing a layout.
- **Do** use the native `<dialog>` for confirmations, with a Danger button for destructive ones.
- **Do** show outcomes with the status pill vocabulary and its fixed color mapping.

### Don't:
- **Don't** add shadows, gradients or a second accent color (The No-Lift and One Warm Note rules).
- **Don't** load web fonts or an icon font; the 🥟 emoji is the only mark.
- **Don't** use red, green or mustard decoratively.
- **Don't** add a UI component library; the plain CSS approach is part of the single-binary constraint.
