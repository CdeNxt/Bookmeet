package handler

import "testing"

// In-person extras are additive on top of the basic buffers, and the drive time only
// ever lands before the meeting.
func TestUseInPersonBuffers(t *testing.T) {
	et := &bookableEventType{
		BufferBeforeMinutes:   5,
		BufferAfterMinutes:    5,
		InPersonBeforeMinutes: 5,
		InPersonAfterMinutes:  10,
		FixedDriveMinutes:     30,
	}
	before, after := et.inPersonReservation()
	if before != 35 || after != 10 {
		t.Fatalf("inPersonReservation = %d/%d, want 35/10", before, after)
	}
}

func TestUseInPersonBuffersNoExtrasIsNoop(t *testing.T) {
	et := &bookableEventType{BufferBeforeMinutes: 5, BufferAfterMinutes: 7}
	et.useInPersonBuffers()
	if et.BufferBeforeMinutes != 5 || et.BufferAfterMinutes != 7 {
		t.Fatalf("buffers changed with no in-person settings: %d/%d", et.BufferBeforeMinutes, et.BufferAfterMinutes)
	}
}

// Slot generation widens busy intervals: BufferAfter keeps time clear before a new start,
// BufferBefore after a new end. Drive + parking therefore feed BufferAfter, wrap-up feeds
// BufferBefore, and the basic buffers are never swapped (5/10 stays 5/10 plus extras).
func TestUseInPersonBuffersIsAdditiveWithoutSwappingBasics(t *testing.T) {
	et := &bookableEventType{
		BufferBeforeMinutes:   5,
		BufferAfterMinutes:    10,
		InPersonBeforeMinutes: 5,
		InPersonAfterMinutes:  3,
		FixedDriveMinutes:     30,
	}
	et.useInPersonBuffers()
	if et.BufferBeforeMinutes != 5+3 || et.BufferAfterMinutes != 10+35 {
		t.Fatalf("buffers = %d/%d, want 8/45", et.BufferBeforeMinutes, et.BufferAfterMinutes)
	}
}
