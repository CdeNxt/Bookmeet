package handler

// In-person meetings reserve extra time on top of the basic buffers: the owner's extra
// before/after minutes (parking, getting settled, wrap-up) and the drive to the venue.

// googleTravel reports whether drive time comes from the Routes API for this request:
// Google mode, with the guest's address known. Until the address is known (and whenever a
// lookup fails) the fixed drive time stands in.
func (et *bookableEventType) googleTravel() bool {
	return et.TravelMode == travelModeGoogle && et.TravelDestination != ""
}

// inPersonReservation is the time held before and after an in-person meeting. In Google
// mode with a known address the drive is added per neighbouring meeting instead (see
// hostAvailability and travelNeighbours), so only the extras are returned here.
func (et *bookableEventType) inPersonReservation() (before, after int) {
	drive := et.FixedDriveMinutes
	if et.googleTravel() {
		drive = 0
	}
	return et.InPersonBeforeMinutes + drive, et.InPersonAfterMinutes
}

func (et *bookableEventType) useInPersonBuffers() {
	before, after := et.inPersonReservation()
	et.applyReservation(before, after)
}

// applyReservation adds time held before and after the meeting to the buffers.
//
// The slot engine, the calendar check and the booking window all read the buffers from
// the point of view of the *other* meeting: BufferAfter is the gap kept clear before a new
// start, BufferBefore the gap kept clear after a new end. So the time needed ahead of this
// meeting (travel, parking) is added to BufferAfter, and the wrap-up after it to
// BufferBefore. The basic buffers stay where they were, so a virtual and an in-person
// meeting treat them identically.
func (et *bookableEventType) applyReservation(before, after int) {
	if before == 0 && after == 0 {
		return
	}
	et.BufferBeforeMinutes += after
	et.BufferAfterMinutes += before
}
