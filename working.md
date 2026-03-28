# Working Notes

## Terminal tracing

- `ION_TERM_TRACE=1` enables the detailed terminal trace and writes to `/tmp/ion-term-trace.log`.
- `ION_TERM_TRACE=/path/to/log` writes the detailed trace to the given path.
- `ION_RENDER_TRACE=1` enables aggregate render stats only.
- If both are unset, there should be no meaningful runtime cost beyond cheap branch checks.

## Current rendering model

- Rendering is only partially incremental.
- `drawBufferMode` uses diff rendering only for:
  - `redrawBufferCursor`
  - `redrawBufferContent`
  - `redrawBufferStatus`
- All other redraw classes are forced to full-frame repaint by `redrawNeedsFullFrame(...)`.
- The diff renderer is currently conservative:
  - row-granular diffing
  - if a row changes, the whole row is repainted
  - no line-motion optimization
  - no character insert/delete optimization

## Freeze debugging status

- The observed "freeze" has not looked like a dead process.
- The strongest trace evidence so far showed the client continuing to read stdin while the UI appeared frozen.
- During that state, input was dominated by SGR mouse motion events (`button=35`), with normal keyboard input not appearing until later.
- Resize/full redraw often "recovers" the UI, which suggests input starvation/backlog rather than a hard deadlock.

## Current mitigation

- Keep full mouse reporting enabled so tmux does not reclaim mouse handling.
- Coalesce buffered runs of passive SGR motion events in the input decoder, keeping only the most recent motion event from a burst.
- Do not dismiss the context menu merely because the pointer moved outside it; dismissal should still require outside click/scroll or the lost-release path.

## Known open issue

- Rendering/input can still feel sluggish intermittently, and the root cause may not be fully understood yet.
- `redrawBufferContent` will likely need to become more incremental eventually, especially for large files and sustained edit/navigation activity.

## Next debugging targets

- Reproduce with `ION_TERM_TRACE=1` using the rebuilt binary.
- Check whether detailed traces still show long runs of mouse motion events starving keyboard input.
- If freezes persist, separate:
  - input starvation/backlog
  - redraw scheduling bugs
  - diff/full-frame rendering interactions

## Lab Notebook

### 2026-03-27 21:22:32 EDT

- Context: active freeze reproduced with `ION_TERM_TRACE=1`, trace written to `/tmp/ion-term-trace.log`.
- Observation: the client was still processing `stdin-ready`, `read-rune`, and redraw events while the UI appeared frozen.
- Observation: the trace window was dominated by passive SGR motion events (`button=35`), with earlier bursts of scroll events (`button=65`).
- Observation: no keyboard input appeared in that trace window, which matches the earlier starvation/backlog hypothesis.
- Interpretation: this still does not look like a hard deadlock; it looks like mouse-input pressure starving more useful input.
- Change: updated the mouse decoder to coalesce timed bursts of passive no-button motion, not just runs that were already buffered contiguously.
- Verification: `go test ./internal/client/term -count=1` passed after the decoder change.

### 2026-03-28 09:38:21 EDT

- Context: freeze reproduced again with `ION_TERM_TRACE`, after allowing `redrawOverlayInput` through the diff renderer while keeping `redrawOverlayHistory` on full-frame repaint.
- Observation: the strongest trace signal was not command typing; `overlay_input` redraws were small diff renders, while the visible stall correlated with repeated HUD-history mouse events.
- Observation: the trace showed many repeated `menu-mouse button=64 ... overlay=true` events, each immediately triggering `redraw class=overlay_history` and `render full class=overlay_history force=true`.
- Observation: some wheel-like events also arrived with button codes such as `66`, which were unsafe because the old overlay handler could alias them to a middle-click style action via `button & 3`.
- Interpretation: the immediate freeze was a redraw storm in the HUD history path, driven by wheel/trackpad traffic over the overlay rather than by `redrawOverlayInput` diff rendering.
- Change: made wheel handling explicit in the mouse path, so only vertical wheel events scroll; unknown wheel codes no longer masquerade as clicks/selections.
- Change: coalesced buffered and timed bursts of repeated vertical wheel events at the same screen position, so one wheel burst produces one larger scroll update instead of many back-to-back redraws.
- Change: kept `redrawOverlayHistory` on full-frame repaint for now; only `redrawOverlayInput` remains diff-rendered.
- Verification: added mouse regressions for passive motion, unknown wheel codes, boundary no-op scrolls, and coalesced wheel bursts; `go test ./internal/client/term -count=1` passed.
