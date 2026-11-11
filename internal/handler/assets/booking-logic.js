// booking-logic.js — the PURE date/slot/format logic shared by book.html and manage.html, so a
// change is made once instead of twice. No DOM.
// Served inlined into the book/manage Go templates, and require()-able by the node tests
// (booking-logic.test.js). Same UMD pattern as room-logic.js — no build step, stays
// framework-free.
//
// NOT loaded by embed.js. The widget is served as its own standalone file
// (internal/handler/embed_handler.go serves the embedded bytes unmodified), so `BookingLogic`
// is undefined inside it and it carries its own copies of the few helpers it needs — see the
// comments on its dowLabels and fmt. Anything added here that all three surfaces need has to be
// mirrored there deliberately.
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.BookingLogic = factory();
})(typeof self !== 'undefined' ? self : this, function () {
  function pad2(n) { return (n < 10 ? '0' : '') + n; }

  // dateKeyFromISO — the "YYYY-MM-DD" a slot belongs to, in the SELECTED timezone. Correct: uses
  // Intl with an explicit tz, NOT new Date().toLocaleDateString() (which keys off the browser tz
  // and was the latent bug in book.html/manage.html). This is the grouping key for slots-by-day.
  function dateKeyFromISO(iso, tz) {
    var p = new Intl.DateTimeFormat('en-CA', {
      timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit'
    }).format(new Date(iso));
    return p; // en-CA already yields YYYY-MM-DD
  }

  // ymd — "YYYY-MM-DD" for a local Date (the calendar grid's own day cells).
  function ymd(d) { return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()); }

  // groupSlotsByDay — { "YYYY-MM-DD": [slot,…] } in the selected tz. Slots are {start,…} (or pass
  // a `key` selector for shapes that differ). Optionally drops one slot (reschedule excludes the
  // current booking's own time).
  function groupSlotsByDay(slots, tz, excludeStart) {
    var by = {};
    (slots || []).forEach(function (s) {
      if (excludeStart && s.start === excludeStart) return;
      var k = dateKeyFromISO(s.start, tz);
      (by[k] = by[k] || []).push(s);
    });
    return by;
  }

  // mergeDaySlots — one day's entries in time order, each tagged `.taken`, for event
  // types that show already-booked times greyed out instead of hiding them.
  //
  // Free and taken arrive as separate arrays from the API and are only ever combined
  // here, for display. Keeping them apart on the wire is deliberate: a merged list is
  // one field away from a client submitting a taken start as a booking.
  function mergeDaySlots(free, taken) {
    var out = [];
    (free || []).forEach(function (s) { out.push(withTaken(s, false)); });
    (taken || []).forEach(function (s) { out.push(withTaken(s, true)); });
    // Parsed rather than string-compared: slot times carry a UTC offset, and two
    // entries on the same calendar day can straddle a DST change and sort wrongly.
    out.sort(function (a, b) { return Date.parse(a.start) - Date.parse(b.start); });
    return out;
  }

  function withTaken(slot, taken) {
    var copy = {};
    for (var k in slot) { if (Object.prototype.hasOwnProperty.call(slot, k)) copy[k] = slot[k]; }
    copy.taken = taken;
    return copy;
  }


  // formatTime — "1:30 PM" in the selected tz.
  function formatTime(iso, tz, locale) {
    return new Intl.DateTimeFormat(locale || [], {
      timeZone: tz, hour: 'numeric', minute: '2-digit'
    }).format(new Date(iso));
  }

  // formatDay — a date label in the selected tz. style 'short' → "Mon, Jan 15"; 'long' →
  // "Monday, January 15".
  function formatDay(iso, tz, style, locale) {
    var long = style === 'long';
    return new Intl.DateTimeFormat(locale || [], {
      timeZone: tz, weekday: long ? 'long' : 'short',
      month: long ? 'long' : 'short', day: 'numeric'
    }).format(new Date(iso));
  }

  // weekStartDay — the host's first day of the week as a JS weekday (0=Sun … 6=Sat), from
  // Settings -> Profile (users.week_start). Anything else, including a missing value,
  // means Monday, which is what these pages always used before the setting reached them.
  function weekStartDay(weekStart) {
    var n = Number(weekStart);
    return (weekStart === undefined || weekStart === null || weekStart === '' || n !== Math.floor(n) || n < 0 || n > 6) ? 1 : n;
  }

  // dowIndex — weekday index counted from the first day of the week (0 = that day), for
  // the calendar grid offset. Monday-first unless weekStart says otherwise.
  function dowIndex(date, weekStart) { return (date.getDay() - weekStartDay(weekStart) + 7) % 7; }

  // dowLabels — weekday header labels (2-char abbreviations) starting on the first day of
  // the week (Monday unless weekStart says otherwise), via Intl for
  // the given locale rather than a hardcoded English array. Replaces the old
  // ['Mo','Tu',…] literal that was duplicated (in English, regardless of visitor
  // language) across book.html/manage.html. 2024-01-01 is an arbitrary fixed Monday
  // anchor — only its weekday matters, not the actual date.
  function dowLabels(locale, weekStart) {
    var labels = [];
    var monday = new Date(Date.UTC(2024, 0, 1));
    var shift = weekStartDay(weekStart) - 1;
    for (var i = 0; i < 7; i++) {
      var d = new Date(monday.getTime() + (i + shift) * 86400000);
      var full = new Intl.DateTimeFormat(locale || [], { weekday: 'short', timeZone: 'UTC' }).format(d);
      labels.push(full.slice(0, 2));
    }
    return labels;
  }

  function startOfMonth(d) { return new Date(d.getFullYear(), d.getMonth(), 1); }
  function endOfMonth(d) { return new Date(d.getFullYear(), d.getMonth() + 1, 0); }
  function addMonths(d, n) { return new Date(d.getFullYear(), d.getMonth() + n, 1); }
  function daysInMonth(year, month) { return new Date(year, month + 1, 0).getDate(); }

  // fmt — argument substitution for the translated strings the booking surfaces render
  // themselves, so the three of them don't each grow their own. Supports exactly the two
  // forms the locale files use for these keys: plain %s, taken in order, and the indexed
  // %[n]s that lets a translation reorder its arguments ("%[1]s has no available times on
  // %[2]s" is date-first in several languages). Server-side, Go's fmt does this job; this
  // is the client half of the same contract.
  //
  // Deliberately not a printf. Accepting %d without implementing number formatting would
  // be worse than not claiming to: the keys these surfaces substitute carry %s only, and
  // internal/i18n's verb-parity test holds every locale to English's verbs.
  //
  // A missing argument renders as an empty string rather than leaving "%s" on screen —
  // visibly wrong copy beats a literal format verb in front of a customer.
  function fmt(template, args) {
    var list = args || [];
    var next = 0;
    return String(template).replace(/%(?:\[(\d+)\])?s/g, function (_match, index) {
      var pick = index ? Number(index) - 1 : next++;
      var value = list[pick];
      return value === undefined || value === null ? '' : String(value);
    });
  }

  // NOTE: there is deliberately no host-label helper here. Each surface builds its own
  // (hostsLabel in book.go for the server-rendered page, in book.html's script for the
  // post-slot-pick rewrite, and in embed.js), because the label needs the resolved locale's
  // separator/conjunction keys and this module is locale-free by design. A copy used to
  // live here, exported and unit-tested but called by nothing — which made it a trap: it
  // hardcoded English " & " and would have silently un-translated the label for anyone who
  // consolidated onto it. If these are ever unified, the shared version must take the
  // locale's list_separator/list_conjunction, not hardcode punctuation.

  return {
    dateKeyFromISO: dateKeyFromISO,
    ymd: ymd,
    groupSlotsByDay: groupSlotsByDay,
    mergeDaySlots: mergeDaySlots,
    fmt: fmt,
    formatTime: formatTime,
    formatDay: formatDay,
    weekStartDay: weekStartDay,
    dowIndex: dowIndex,
    dowLabels: dowLabels,
    startOfMonth: startOfMonth,
    endOfMonth: endOfMonth,
    addMonths: addMonths,
    daysInMonth: daysInMonth
  };
});
