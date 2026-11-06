# BookMeet design package

Prepared November 6, 2026, from Bookmeet source commit `432432410cadbe7326d5c080b86ccc89f96d7671` (Bookmeet .6).

## Upload to Claude

Start with `CLAUDE-CRM-BRIEF.md`, `design-tokens.json`, `DESIGN-GUIDE.md`, the black/white SVG logos, and the dark/light desktop PNG backgrounds. Add the CSS and JS when implementing. If Claude cannot read the ZIP, unzip it and upload those files individually. The full native skin and component styles are included as implementation references.

Open `preview.html` in a browser for a working local demonstration of the background, theme switching, logo variants, and panel styling. There are no external scripts or network requests. No application credentials or deployment notes are included.

## Contents

- `logos/`: pure black and pure white transparent PNG and outlined SVG wordmarks, plus icon-only marks. `bookmeet-original-logo.png` is the original charcoal raster artwork, retained for provenance.
- `backgrounds/`: dark/light desktop (2560×1440), header (1600×480), and portrait/mobile assets in SVG and PNG, plus the original static email banner. Static exports use the live particle painter geometry at time zero; animation and pointer interaction are provided separately in JS.
- `css/bookmeet-tokens.css`: reusable CRM-friendly theme variables.
- `css/bookmeet-background.css`: portable animated background styles.
- `css/bookmeet-public-skin.css`: exact current booking skin, including selectors specific to the booking DOM.
- `css/booking-components.css`: current booking UI foundation.
- `js/bookmeet-theme.js`: Light/Dark/Auto preferences and browser persistence.
- `js/bookmeet-background.js`: portable extraction of the current mouse-responsive canvas painter.
- `js/bookmeet-effects.js`: optional finite confetti/unicorn/sad effects. Call `BookMeetEffects('confetti')`, `'unicorns'`, `'both'`, `'sad'`, or `'off'` after a real successful action.
- `source/`: intact native skin and admin app stylesheet for reference. The admin stylesheet uses Tailwind and project dependencies; it is not a standalone CSS bundle.
- `reference/`: screenshots of the actual public pages. These show layout context and are not source artwork.

## Implementation notes

Use the black logo on light surfaces and white on dark. Outlined SVG wordmarks are vector recreations using the original Helvetica Neue lettering and three-wave construction; they are not a pixel trace of the PNG. SVGs contain paths rather than live text, so no font install is needed. Font files are not bundled.

The JS painter intentionally runs at about 25 fps, pauses when hidden, and respects reduced motion. It uses a fixed canvas below content, never intercepting clicks. Light is the exact RGB inverse of the dark painter. Static backgrounds have opaque fills; logos have transparent backgrounds. SVG backgrounds preserve their dots as editable vector rectangles.

For a CRM, adapt the visual language to its data density. Keep navigation, tables, forms, and content on readable panels; reserve the expressive background for page margins, empty states, and headers. Avoid placing dense text directly on moving particles.

This package contains the BookMeet identity and implementation. It does not include Mercury logos, proprietary site assets, or its source code. Upstream Calnode license and notices are retained in `source/`; preserve any notices that apply to reused implementation code.
