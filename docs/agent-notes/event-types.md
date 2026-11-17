# Event types

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

## Event types: slot interval is not the duration

`slot_interval_minutes` = how often a booking may **start**. `duration_minutes` = how long it
**runs**. Deliberately independent (a 45-min meeting offered on the hour is valid). New event
types default the interval to the duration; existing ones keep what is stored, so a change of
default is never retroactive. `slots.Generate` refuses a non-positive interval, so create and
update both validate it - an unvalidated `0` yields an event type with no bookable times and
no explanation. Reported as issue #13, where the setting being absent from the admin UI looked
exactly like duration being ignored.

Keep the editor's floor aligned with the API's (`>= 1`). A stricter client-side minimum makes
an event type configured below it via the API unsaveable from the editor, even when the person
is editing an unrelated field.

**The general rule, learned twice:** the admin editor submits the WHOLE form on every save,
so validating a field merely because the request mentions it validates fields the operator
never touched. Any event type holding a stored value the current rules reject then becomes
unsaveable entirely, with an error pointing at something unrelated. Validate on **change**
(effective value vs stored), not on **mention**. Rows reach those states legitimately: a
provider disconnected after the fact, a duplicate that inherited one (#22), a seeder writing
straight to the table, or a create path that defaulted the field before a rule tightened.

The other half of the same rule: **anything written without validation must be valid by
construction.** `CreateEventType` skips `validateLocation` when it defaults the location,
because there is no request field to blame an error on - so every branch of
`smartDefaultLocation` has to return a type the owner can actually host at. It used to end
at an unconditional `"zoom"`.
