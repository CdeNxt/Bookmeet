package handler

import (
	_ "embed"
	"strings"

	"github.com/calnode/calnode/internal/buildinfo"
)

// sharedPartialsSrc holds the template partials shared by the book, manage and directory
// public pages (see templates/_shared.html): the business header and consent/tracking/footer chrome
// (trackingHead, consentBanner, legalFooter) plus structural parts (calendarGrid).
// It is parsed into all three template sets so these pieces have a single source.
//
//go:embed templates/_shared.html
var sharedPartialsSrc string

//go:embed assets/bookmeet-effects.js
var bookmeetEffectsJS string

//go:embed assets/bookmeet-skin.html
var bookmeetSkinHTML string

// The skin shows the build version in small type at the foot of every public page, so
// it is obvious which release is live. The binary is stamped with the version at build
// time (CI reads packaging/home-assistant/version.txt), so every push that gets deployed
// carries its own number. "dev" and empty builds show nothing.
func init() {
	v := strings.TrimSpace(buildinfo.Version)
	if v == "" || v == "dev" {
		v = ""
	}
	bookmeetSkinHTML = strings.ReplaceAll(bookmeetSkinHTML, "__BM_VERSION__", v)
}
