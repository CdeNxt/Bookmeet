package handler

// Reserve the trip to the venue plus any extra in-person preparation and wrap-up.
func (et *bookableEventType) inPersonReservation() (before, after int) {
	return et.InPersonBeforeMinutes + et.FixedDriveMinutes, et.InPersonAfterMinutes
}

func (et *bookableEventType) useInPersonBuffers() {
	before, after := et.inPersonReservation()
	if before == 0 && after == 0 {
		return
	}
	// The slot engine, the calendar check and the booking window all read the buffers
	// from the point of view of the *other* meeting: BufferAfter is the gap kept clear
	// before a new start, BufferBefore the gap kept clear after a new end. So the
	// time needed ahead of this meeting (travel, parking) is added to BufferAfter, and
	// the wrap-up after it to BufferBefore. The basic buffers stay where they were,
	// so a virtual and an in-person meeting treat them identically.
	et.BufferBeforeMinutes += after
	et.BufferAfterMinutes += before
}
