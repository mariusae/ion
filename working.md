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
