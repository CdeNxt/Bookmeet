package handler

import (
	"fmt"
	"strings"
)

// Why a time will not book, in words: for the owner on a card, and for their agent. Only
// the kind of thing in the way is ever named, never what is on the calendar.

func noticeWords(minutes int) string {
	switch {
	case minutes%1440 == 0 && minutes > 0:
		return fmt.Sprintf("%d-day", minutes/1440)
	case minutes%60 == 0 && minutes > 0:
		return fmt.Sprintf("%d-hour", minutes/60)
	}
	return fmt.Sprintf("%d-minute", minutes)
}

// ownerReasons is each reason as a sentence to the owner.
func (v timeVerdict) ownerReasons() []string {
	var out []string
	if v.Rules.TooSoon {
		out = append(out, "It's sooner than your "+noticeWords(v.MinNotice)+" minimum notice.")
	}
	if v.Rules.OutsideHours {
		out = append(out, "It's outside the hours you take meetings.")
	}
	if v.Rules.TooFar {
		out = append(out, "It's further ahead than you take bookings.")
	}
	if v.Outside && v.Rules == (timeRules{}) {
		out = append(out, "It's outside your booking rules.")
	}
	switch {
	case v.Clash && v.BufferOnly:
		out = append(out, "It's too close to something on your calendar: inside the buffer you keep around meetings.")
	case v.Clash:
		out = append(out, "You have something on your calendar then.")
	}
	return out
}

// agentReason is the same as one clause about the owner, for the agent.
func (v timeVerdict) agentReason(when string) string {
	var out []string
	if v.Rules.TooSoon {
		out = append(out, when+" is sooner than the "+noticeWords(v.MinNotice)+" minimum notice the owner requires")
	}
	if v.Rules.OutsideHours || v.Rules.TooFar || v.Outside && v.Rules == (timeRules{}) {
		out = append(out, when+" is outside the times the owner takes meetings")
	}
	switch {
	case v.Clash && v.BufferOnly:
		out = append(out, "the owner already has something close to "+when+", inside the buffer they keep around meetings")
	case v.Clash:
		out = append(out, "the owner already has something at "+when)
	}
	return strings.Join(out, ", and ")
}

// beyond reports whether v has a reason the owner was not shown in shown.
func (v timeVerdict) beyond(shown timeVerdict) bool {
	return v.Rules.TooSoon && !shown.Rules.TooSoon ||
		v.Rules.OutsideHours && !shown.Rules.OutsideHours ||
		v.Rules.TooFar && !shown.Rules.TooFar ||
		v.Outside && !shown.Outside ||
		v.Clash && !shown.Clash ||
		v.Clash && !v.BufferOnly && shown.BufferOnly
}

// shownReasons is what a Telegram panel showed the owner, kept with the panel so that
// "Book anyway" waives that and nothing more. Kinds only: never what is on the calendar.
type shownReasons struct {
	Outside      bool `json:"outside,omitempty"`
	TooSoon      bool `json:"too_soon,omitempty"`
	TooFar       bool `json:"too_far,omitempty"`
	OutsideHours bool `json:"outside_hours,omitempty"`
	Clash        bool `json:"clash,omitempty"`
	BufferOnly   bool `json:"buffer_only,omitempty"`
}

func (v timeVerdict) shown() *shownReasons {
	return &shownReasons{Outside: v.Outside, TooSoon: v.Rules.TooSoon, TooFar: v.Rules.TooFar,
		OutsideHours: v.Rules.OutsideHours, Clash: v.Clash, BufferOnly: v.BufferOnly}
}

func (s shownReasons) verdict() timeVerdict {
	return timeVerdict{Outside: s.Outside, Clash: s.Clash, BufferOnly: s.BufferOnly,
		Rules: timeRules{TooSoon: s.TooSoon, TooFar: s.TooFar, OutsideHours: s.OutsideHours}}
}
