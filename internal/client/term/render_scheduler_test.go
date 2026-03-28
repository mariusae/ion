package term

import "testing"

func TestRenderSchedulerCoalescesToLatestClassAndStrongestMode(t *testing.T) {
	t.Parallel()

	var scheduler renderScheduler
	scheduler.Request(redrawBufferCursor, false)
	scheduler.Request(redrawOverlayInput, false)
	scheduler.Request(redrawRecover, true)

	class, forceFull, ok := scheduler.Drain()
	if !ok {
		t.Fatalf("Drain() ok = false, want true")
	}
	if got, want := class, redrawRecover; got != want {
		t.Fatalf("Drain() class = %q, want %q", got, want)
	}
	if !forceFull {
		t.Fatalf("Drain() forceFull = false, want true")
	}
	if scheduler.Pending() {
		t.Fatalf("Pending() = true after Drain, want false")
	}
}
