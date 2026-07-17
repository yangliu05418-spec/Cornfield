---
version: alpha
name: "Higgsfield Dark Creative"
description: "Higgsfield AI is a dark-first AI creative suite with a near-black (#0f1113–#14151a) page canvas, electric lime (#d1fe17) as the singular brand accent, and a dense media-card grid layout. Typography is system sans-serif (ui-sans-serif) for UI chrome with Space Grotesk for display headings. Cards use 12px and 8px radii, surfaces are layered with subtle glass inset shadows, and the lime accent drives all primary CTAs, badges, and highlight text."
colors:
  cyan-accent: "#18dcff"
  dark-ink: "#131517"
  magenta-accent: "#ff005b"
  page-background: "#0f1113"
  surface-primary: "#1c1e20"
  surface-secondary: "#23262a"
  lime-accent: "#d1fe17"
  text-primary: "#f7f7f8"
  text-secondary: "#898a8b"
  text-tertiary: "#a8a8a8"
  text-white: "#ffffff"
  border-subtle: "#2e3031"
  page-background-light: "#f7f7f8"
  surface-white: "#ffffff"
  text-dark: "#131517"
typography:
  body-default:
    fontFamily: "ui-sans-serif"
    fontSize: "16px"
    fontWeight: "400"
    lineHeight: "24px"
  body-medium:
    fontFamily: "ui-sans-serif"
    fontSize: "16px"
    fontWeight: "500"
    lineHeight: "24px"
  label-semibold:
    fontFamily: "ui-sans-serif"
    fontSize: "14px"
    fontWeight: "600"
    lineHeight: "20px"
  label-medium:
    fontFamily: "ui-sans-serif"
    fontSize: "14px"
    fontWeight: "500"
    lineHeight: "20px"
  caption-semibold:
    fontFamily: "ui-sans-serif"
    fontSize: "12px"
    fontWeight: "600"
    lineHeight: "18px"
  micro-label:
    fontFamily: "ui-sans-serif"
    fontSize: "10px"
    fontWeight: "500"
    lineHeight: "14px"
    letterSpacing: "0.2px"
  display-heading:
    fontFamily: "Space Grotesk"
    fontSize: "28px"
    fontWeight: "700"
    lineHeight: "36px"
  display-caption:
    fontFamily: "Space Grotesk"
    fontSize: "12px"
    fontWeight: "700"
    lineHeight: "18px"
  ui-label-inter:
    fontFamily: "Inter"
    fontSize: "12px"
    fontWeight: "500"
    lineHeight: "16px"
rounded:
  radius-sm: "4px"
  radius-md: "6px"
  radius-card-sm: "8px"
  radius-card: "12px"
  radius-panel: "16px"
  radius-lg: "20px"
  radius-xl: "24px"
  radius-2xl: "32px"
  radius-pill: "9999px"
spacing:
  space-1: "2px"
  space-2: "4px"
  space-3: "6px"
  space-4: "8px"
  space-5: "10px"
  space-6: "12px"
  space-7: "16px"
  space-8: "20px"
  space-9: "24px"
  space-10: "32px"
  space-11: "36px"
  space-12: "40px"
  space-13: "64px"
---

## Overview

Higgsfield AI is a dark-first AI creative suite with a near-black (#0f1113–#14151a) page canvas, electric lime (#d1fe17) as the singular brand accent, and a dense media-card grid layout. Typography is system sans-serif (ui-sans-serif) for UI chrome with Space Grotesk for display headings. Cards use 12px and 8px radii, surfaces are layered with subtle glass inset shadows, and the lime accent drives all primary CTAs, badges, and highlight text.

**Signature traits:**
- Dual typeface system: Pairs ui-sans-serif and Space Grotesk across the type hierarchy.
- Soft, rounded geometry: Generous corner rounding up to 9999px.

## Colors

The palette uses 18 validated color tokens across 2 theme profiles. Semantic roles stay attached to observed usage so generation agents can choose accents without inventing new color meaning.

**Semantic naming:**
- **surface-background** maps to `page-background`: Role "background" is grounded by usage context "Primary page/canvas background, full-bleed sections".
- **content-background** maps to `surface-secondary`: Role "background" is grounded by usage context "Secondary panels, download panel, glass overlays".
- **content-text** maps to `text-primary`: Role "text" is grounded by usage context "Primary body text, nav labels, card titles".
- **action-text** maps to `text-white`: Role "text" is grounded by usage context "High-emphasis headings, button labels on dark surfaces".

### Dark Theme

### Primary Brand
- **Cyan Accent** (#18dcff): Occasional highlight, secondary accent in media overlays. Role: accent. {authored: rgb(24, 220, 255), space: rgb}

### Text Scale
- **Lime Accent** (#d1fe17): Primary CTA buttons, badges, highlight text, focus ring glow, pricing tag. Role: text. {authored: rgb(209, 254, 23), space: rgb, alpha: 0.05}
- **Text Primary** (#f7f7f8): Primary body text, nav labels, card titles. Role: text. {authored: rgb(247, 247, 248), space: rgb}
- **Text Secondary** (#898a8b): Secondary descriptive text, icon labels. Role: text. {authored: rgb(137, 138, 139), space: rgb}
- **Text Tertiary** (#a8a8a8): Tertiary labels, muted metadata. Role: text. {authored: rgb(168, 168, 168), space: rgb}
- **Text White** (#ffffff): High-emphasis headings, button labels on dark surfaces. Role: text. {authored: rgb(255, 255, 255), space: rgb, alpha: 0.05}

### Interactive
- **Border Subtle** (#2e3031): Card borders, dividers, hairlines. Role: border. {authored: rgb(46, 48, 49), space: rgb, alpha: 0.48}

### Surface & Shadows
- **Dark Ink** (#131517): Button text on lime CTA, icon fills on inverted surfaces. Role: background. {authored: rgb(19, 21, 23), space: rgb}
- **Magenta Accent** (#ff005b): Trending badge, promotional highlight labels. Role: background. {authored: rgb(255, 0, 91), space: rgb}
- **Page Background** (#0f1113): Primary page/canvas background, full-bleed sections. Role: background. {authored: rgb(15, 17, 19), space: rgb}
- **Surface Primary** (#1c1e20): Card and panel surfaces, nav bar background. Role: background. {authored: rgb(28, 30, 32), space: rgb}
- **Surface Secondary** (#23262a): Secondary panels, download panel, glass overlays. Role: background. {authored: rgb(35, 38, 42), space: rgb, alpha: 0.75}

### Light Theme

### Text Scale
- **Lime Accent** (#d1fe17): CTA buttons and highlights remain lime in light mode. Role: text. {authored: rgb(209, 254, 23), space: rgb, alpha: 0.05}
- **Text Dark** (#131517): Primary text in light mode. Role: text. {authored: rgb(19, 21, 23), space: rgb}
- **Text Secondary** (#898a8b): Secondary text in light mode. Role: text. {authored: rgb(137, 138, 139), space: rgb}

### Interactive
- **Border Subtle** (#2e3031): Dividers and card borders in light mode. Role: border. {authored: rgb(46, 48, 49), space: rgb, alpha: 0.48}

### Surface & Shadows
- **Page Background Light** (#f7f7f8): Light mode page canvas (same token, inverted role). Role: background. {authored: rgb(247, 247, 248), space: rgb}
- **Surface White** (#ffffff): Card surfaces in light mode. Role: background. {authored: rgb(255, 255, 255), space: rgb, alpha: 0.05}

## Typography

Typography uses ui-sans-serif, Space Grotesk, Inter across extracted hierarchy roles. Keep hierarchy mapped to these token rows before adding decorative type styles.

Mixes ui-sans-serif and Space Grotesk and Inter for visual contrast. Weight range spans regular, medium, semi-bold, bold. Sizes range from 10px to 28px.

### Type Scale Evidence
| Role | Font | Size | Weight | Line Height | Letter Spacing | Stack / Features | Notes |
|------|------|------|--------|-------------|----------------|------------------|-------|
| Primary body copy, nav items, card descriptions | ui-sans-serif | 16px | 400 | 24px | normal | ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji, ui-sans-serif, system-ui, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Emphasized body text, section intros | ui-sans-serif | 16px | 500 | 24px | -1% | ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji, ui-sans-serif, system-ui, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Nav labels, card titles, button text | ui-sans-serif | 14px | 600 | 20px | normal | ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji, ui-sans-serif, system-ui, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Secondary labels, metadata, tag text | ui-sans-serif | 14px | 500 | 20px | 0% | ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji, ui-sans-serif, system-ui, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Badge labels, small UI captions | ui-sans-serif | 12px | 600 | 18px | 0% | ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji, ui-sans-serif, system-ui, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Tiny status labels, category chips | ui-sans-serif | 10px | 500 | 14px | 0.2px | ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji, ui-sans-serif, system-ui, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Section display headings, hero titles | Space Grotesk | 28px | 700 | 36px | -2% | Space Grotesk, Space Grotesk Fallback, ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Uppercase badge labels, promo tags in Space Grotesk | Space Grotesk | 12px | 700 | 18px | 0% | Space Grotesk, Space Grotesk Fallback, ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif, Apple Color Emoji, Segoe UI Emoji, Segoe UI Symbol, Noto Color Emoji | Extracted token |
| Cookie consent and toast UI labels | Inter | 12px | 500 | 16px | normal | Inter, Inter Fallback, system-ui, -apple-system, Segoe UI, Roboto, Ubuntu, Cantarell, Noto Sans, sans-serif | Extracted token |

## Layout

Responsive system uses 2 breakpoint tier(s): mobile, desktop.

This system uses a 4px base grid with scale values 2, 4, 6, 8, 10, 12, 16, 20, 24, 32, 36, 40, 64.

### Responsive Strategy
- **mobile (<= 767px)**: Constrain layout for small viewports and prioritize vertical stacking.
- **desktop (Unknown)**: Expand layout density and horizontal composition for wide viewports.

### Spacing System
| Token | Value | Px | Notes |
|------|-------|----|-------|
| space-1 | 2px | 2 | Extracted spacing token |
| space-2 | 4px | 4 | Extracted spacing token |
| space-3 | 6px | 6 | Extracted spacing token |
| space-4 | 8px | 8 | Extracted spacing token |
| space-5 | 10px | 10 | Extracted spacing token |
| space-6 | 12px | 12 | Extracted spacing token |
| space-7 | 16px | 16 | Extracted spacing token |
| space-8 | 20px | 20 | Extracted spacing token |
| space-9 | 24px | 24 | Extracted spacing token |
| space-10 | 32px | 32 | Extracted spacing token |
| space-11 | 36px | 36 | Extracted spacing token |
| space-12 | 40px | 40 | Extracted spacing token |
| space-13 | 64px | 64 | Extracted spacing token |

## Elevation & Depth

Keep depth flat unless validated shadow or interaction evidence appears in the extraction payload. Do not invent shadows beyond this evidence boundary.

### Shadow Evidence
| Shadow Token | Layers | Details |
|--------------|--------|---------|
| n/a | 0 | No validated shadow payload |

### Interaction Signals
| Theme | Signal | Evidence |
|-------|--------|----------|
| Light | backdrop-filter | blur(40px) ; blur(8px) ; blur(12px) |
| Light | outline-color | rgb(247, 247, 248) ; rgb(255, 255, 255) ; rgba(0, 0, 0, 0) |
| Light | outline-width | 3px |
| Light | outline-offset | 0px |
| Light | transform | matrix(1, 0, 0, 1, 0, 0) ; matrix(1, 0, -0.212557, 1, 0, 0) ; matrix(1, 0, -0.190765, 0.981636, 11.5381, 0) |
| Dark | backdrop-filter | blur(40px) ; blur(8px) ; blur(12px) |
| Dark | outline-color | rgb(247, 247, 248) ; rgb(255, 255, 255) ; rgba(0, 0, 0, 0) |
| Dark | outline-width | 3px |
| Dark | outline-offset | 0px |
| Dark | transform | matrix(1, 0, 0, 1, 0, 0) ; matrix(1, 0, -0.212557, 1, 0, 0) ; matrix(1, 0, -0.190765, 0.981636, 11.5381, 0) |

## Shapes

Shape language maps directly to rounded tokens. Keep component corners consistent with the role mapping below before introducing bespoke geometry.

### Radius Roles
| Token | Value | Px | Role Mapping |
|------|-------|----|--------------|
| radius-sm | 4px | 4 | Subtle corner |
| radius-md | 6px | 6 | Subtle corner |
| radius-card-sm | 8px | 8 | Control corner |
| radius-card | 12px | 12 | Control corner |
| radius-panel | 16px | 16 | Card corner |
| radius-lg | 20px | 20 | Card corner |
| radius-xl | 24px | 24 | Large surface corner |
| radius-2xl | 32px | 32 | Large surface corner |
| radius-pill | 9999px | 9999 | Large surface corner |

### Geometry Evidence
| Radius Token | Shape | Units |
|--------------|-------|-------|
| radius-sm | 4px | px |
| radius-md | 6px | px |
| radius-card-sm | 8px | px |
| radius-card | 12px | px |
| radius-panel | 16px | px |
| radius-lg | 20px | px |
| radius-xl | 24px | px |
| radius-2xl | 32px | px |
| radius-pill | 9999px | px |

## Components

(none detected)

## Do's and Don'ts

Guardrails protect Dual typeface system, Soft, rounded geometry without adding unsupported visual claims.

| Do | Don't |
|----|---------|
| Do maintain consistent spacing using the base grid | Don't make unsupported claims about absent visual features |
| Do maintain WCAG AA contrast ratios (4.5:1 for normal text) | Don't mix rounded and sharp corners in the same view |
| Do use the primary color only for the single most important action per screen |  |
| Do verify evidence before writing new design-system guidance |  |

## Responsive Evidence

### Breakpoints
| Name | Width | Key Changes |
|------|-------|-------------|
| Mobile | <= 440px | only screen and (max-width: 440px) |
| Mobile | <= 480px | only screen and (max-width: 480px) |
| Mobile | <= 600px | (max-width: 600px) |
| Mobile | <= 767px | (max-width: 767px) |
| Breakpoint 5 | Unknown | (hover: hover) and (pointer: fine) |

## Agent Prompt Guide

### Example Component Prompts
- Create button component using validated primary color role and spacing tokens.
- Create card component with mapped radius role and evidence-backed elevation.
- Create form input component using inferred typography hierarchy and border roles.

### Iteration Guide
1. Start with extracted palette and typography roles only.
2. Map spacing and radius directly from token tables before visual polish.
3. Apply component patterns one section at a time and compare against source intent.
4. Keep elevation claims tied to explicit evidence in output.
5. Iterate with smallest diffs and re-check section hierarchy after each change.
