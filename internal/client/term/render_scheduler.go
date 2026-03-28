package term

type renderInvalidation uint32

const (
	renderInvalidateNone   renderInvalidation = 0
	renderInvalidateBuffer renderInvalidation = 1 << iota
	renderInvalidateOverlayHistory
	renderInvalidateOverlayInput
	renderInvalidateMenu
)

const renderInvalidateAllLayers = renderInvalidateBuffer | renderInvalidateOverlayHistory | renderInvalidateOverlayInput | renderInvalidateMenu

type renderRequest struct {
	class        redrawClass
	forceFull    bool
	invalidation renderInvalidation
}

type renderScheduler struct {
	pending bool
	req     renderRequest
}

// renderRequestForLayers is the primary semantic render API for the live compositor path.
func renderRequestForLayers(class redrawClass, invalidation renderInvalidation) renderRequest {
	return renderRequest{
		class:        class,
		invalidation: invalidation,
	}
}

func fullRenderRequest(class redrawClass) renderRequest {
	return renderRequest{
		class:        class,
		forceFull:    true,
		invalidation: renderInvalidateAllLayers,
	}
}

func bufferRenderRequest(class redrawClass, _ *bufferState, overlay *overlayState, menu *menuState, focused bool) renderRequest {
	req := renderRequestForLayers(class, renderInvalidateBuffer)
	if class != redrawBufferCursor {
		return req
	}
	req.invalidation = renderInvalidateNone
	if bufferInactive(overlay, menu, focused) {
		req.invalidation = renderInvalidateBuffer
	}
	return req
}

func (s *renderScheduler) Request(req renderRequest) {
	if s == nil {
		return
	}
	s.pending = true
	s.req.class = req.class
	s.req.forceFull = s.req.forceFull || req.forceFull
	s.req.invalidation |= req.invalidation
}

func (s *renderScheduler) Pending() bool {
	return s != nil && s.pending
}

func (s *renderScheduler) Drain() (renderRequest, bool) {
	if s == nil || !s.pending {
		return renderRequest{}, false
	}
	req := s.req
	s.pending = false
	s.req = renderRequest{}
	return req, true
}

func (r renderRequest) invalidates(mask renderInvalidation) bool {
	return r.invalidation&mask != 0
}

// buildRenderRequest maps legacy redraw classes onto semantic invalidations.
// New live-path code should prefer the explicit request constructors above.
func buildRenderRequest(class redrawClass, forceFull bool, state *bufferState, overlay *overlayState, menu *menuState, focused bool) renderRequest {
	req := renderRequest{
		class:     class,
		forceFull: forceFull,
	}
	switch class {
	case redrawBufferCursor:
		req = bufferRenderRequest(class, state, overlay, menu, focused)
		req.forceFull = forceFull
	case redrawBufferSelection, redrawBufferViewport, redrawBufferContent, redrawBufferStatus:
		req = bufferRenderRequest(class, state, overlay, menu, focused)
		req.forceFull = forceFull
	case redrawOverlayInput:
		req.invalidation = renderInvalidateOverlayInput
	case redrawOverlayHistory:
		req.invalidation = renderInvalidateOverlayHistory
	case redrawOverlayOpen, redrawOverlayClose:
		req.invalidation = renderInvalidateBuffer | renderInvalidateOverlayHistory | renderInvalidateOverlayInput
	case redrawMenuHover, redrawMenuOpen, redrawMenuClose:
		req.invalidation = renderInvalidateMenu
	case redrawTheme, redrawRefresh:
		req.invalidation = renderInvalidateAllLayers
	default:
		req.invalidation = renderInvalidateAllLayers
	}
	return req
}
