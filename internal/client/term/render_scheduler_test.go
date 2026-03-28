package term

import "testing"

func TestRenderSchedulerCoalescesToLatestClassAndStrongestMode(t *testing.T) {
	t.Parallel()

	var scheduler renderScheduler
	scheduler.Request(renderRequest{class: redrawBufferCursor, invalidation: renderInvalidateNone})
	scheduler.Request(renderRequest{class: redrawOverlayInput, invalidation: renderInvalidateOverlayInput})
	scheduler.Request(renderRequest{class: redrawRecover, forceFull: true, invalidation: renderInvalidateAllLayers})

	req, ok := scheduler.Drain()
	if !ok {
		t.Fatalf("Drain() ok = false, want true")
	}
	if got, want := req.class, redrawRecover; got != want {
		t.Fatalf("Drain() class = %q, want %q", got, want)
	}
	if !req.forceFull {
		t.Fatalf("Drain() forceFull = false, want true")
	}
	if got, want := req.invalidation, renderInvalidateAllLayers; got != want {
		t.Fatalf("Drain() invalidation = %v, want %v", got, want)
	}
	if scheduler.Pending() {
		t.Fatalf("Pending() = true after Drain, want false")
	}
}

func TestBufferRenderRequestCursorSkipsLayerWhenCursorNotPainted(t *testing.T) {
	t.Parallel()

	state := &bufferState{dotStart: 1, dotEnd: 1}
	req := bufferRenderRequest(redrawBufferCursor, state, nil, newMenuState(), true)
	if got, want := req.invalidation, renderInvalidateNone; got != want {
		t.Fatalf("bufferRenderRequest() invalidation = %v, want %v", got, want)
	}
}
