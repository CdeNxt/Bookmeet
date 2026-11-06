# BookMeet visual language

## Character
Architectural, quiet, precise, personal. Layered rectangular particle planes move like a gently twisting structure. Large clear typography and rounded translucent panels sit in front. Periwinkle marks the main action. The CRM should feel like the same product family without forcing a booking hero above every screen.

## Palette
| Role | Dark | Light |
|---|---|---|
| Page | #15161E | #EAE9E1 |
| Primary action | #5965E9 | #5965E9 |
| Hover action | #6A75F2 | #6A75F2 |
| Main text | #F2F2F7 | #252735 |
| Hero heading | #F5F5FB | #242735 |
| Secondary copy | #B6B7C7 | #5C6376 |
| Card | rgba(25,27,38,.96) | rgba(255,255,255,.94) |
| Card border | #FFFFFF20 | #CDD1DF |
| Slot/control | #232637 | #F8F8FD |

The animation's base color inverts from RGB(21,22,30) to RGB(234,233,225). Use the same geometry and motion in both modes.

## Typography and spacing
Use Inter if already available; otherwise the system sans stack in the CSS. No font downloads are required. Hero 40–76px, weight 500, line height 1.04, tracking −.055em. UI/body 13–15px; secondary metadata 11–12px. Uppercase eyebrow: 11px, .16em letter spacing. In a CRM, use smaller headings and keep tables legible. Use 8px spacing rhythm; panels generally have 24–32px padding.

## Shape
Cards 20px radius. Inner tiles 12px. Inputs/time buttons 9px. Primary/secondary actions and appearance controls use pill radii. Use thin low-contrast borders, restrained shadows, and 14px backdrop blur on large public panels. Focus rings must remain visible in both modes.

## Logos
Three gently sagging parallel waves to the left of the BookMeet wordmark. Preserve the proportions. Use pure black on light and pure white on dark; leave at least one wave-height of clear space. Use the mark alone where the wordmark is too small. Avoid blurring or adding a glow to the identity. Use SVG for product UI and PNG where vector upload is unsupported.

## Motion
Pointer movement produces eased parallax and repels nearby particles within a 200px radius. Never make motion a requirement for understanding information. Reduced motion gets a static frame. Pause when hidden. Use finite celebrations sparingly, after confirmed success, with an Off option. Keep the background below content and pointer-events disabled.

## CRM adaptation
Use a compact sidebar/top bar, clear record hierarchy, search, filters, readable tables, and restrained status chips. Keep the periwinkle primary action consistent. Animated full-viewport backgrounds can remain behind calm opaque panels. Retain Light/Dark/Auto and visible keyboard focus. Use actual working navigation and actions; avoid decorative buttons.
