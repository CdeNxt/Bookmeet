# Conversational travel booking

**Status** — Discovery. Keep this separate from the booking UI stability release.

## Outcome

Bookmeet offers in-person times the host can actually make, taking into account the trip to the meeting and the trip to the next commitment. Virtual calls at the same workspace can run back to back when basic buffers allow. Guests can use a short conversation to settle on a place and time. Hosts can later opt into itinerary suggestions by text.

## Two approaches

- **Calendar rules with a default workspace.** Treat physical events as location anchors. Assume virtual calls happen at the host's saved workspace unless explicitly assigned elsewhere. This provides a predictable baseline and works without host replies.
- **Conversational itinerary.** Let the guest specify a place or area and a date range in chat. Ask only for missing details, then offer two or three feasible options. Keep a private day plan with fixed, default, and flexible locations. After booking, propose useful changes such as working nearby until 4:30 and driving home afterward.

Start with the first approach as the scheduling baseline. Add the guest conversation and a private host choice in Bookmeet, then consider SMS. Both approaches should use the same availability engine.

## Scheduling behavior

- Read busy intervals and usable locations from connected calendars and Bookmeet. A virtual or unlocated event still blocks time. An address is a location anchor only when it describes where the host actually needs to be.
- Evaluate both sides of a candidate time. For home call → coffee shop → home call, check both drives. For venue A → venue B, route directly. A new virtual booking also needs to leave time to reach any fixed commitments.
- Use departure-time traffic estimates. Use historical conditions for advance forecasts and fresh conditions near departure where available. Keep basic before and after buffers separate from additive in-person arrival and departure allowances, such as parking or setup. Support a configured fixed fallback and withhold uncertain slots when no safe fallback exists.
- Revalidate when confirming or rescheduling. Protect the calendar and location assumptions used to offer the slot. Avoid exposing the host's private addresses, calendar titles, or itinerary details to guests.

## Host decisions and assistant integration

A confirmed booking must work with the current day plan. If a slot only works when the host agrees to change workspace, get approval before confirming it or show the guest other feasible options. A post-booking question can improve a plan that already works.

> Bill booked 1:30 across town. Your next call is at 3:30. Head home after the meeting, or work nearby until 4:30 and drive home then?

No reply keeps the current plan. A confirmed choice changes the private workspace window and recalculates availability without moving existing meetings or updating attendee-facing details. An engine result should include an internal explanation and viable alternatives so the web app and future SMS assistant can present the same choices.

## Open questions

- How should a host mark a virtual call as flexible or choose a different workspace for one day?
- When should Bookmeet ask about working nearby, and what venues can the host approve for calls?
- Should travel reserve time only inside Bookmeet or also create private busy blocks on the connected calendar?
- What traffic uncertainty, route quota, and host-response timeout should govern offers that depend on a new plan?
