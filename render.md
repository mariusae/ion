# Renderer Redesign

## Why This Needs A Redesign

The current renderer is brittle because it is centered on rebuilding a full
logical frame and then diffing snapshots after the fact.

Today the pipeline is roughly:

1. Mutate editor state.
2. Build a full `terminalFrame`.
3. Compare it against the previous `terminalFrame`.
4. Guess whether the change can be expressed as row repaint, row shift, or full
   redraw.

This has a few structural problems:

- Input handling and redraw policy are tightly coupled.
- Viewport scrolling is inferred after rendering instead of being represented as
  a first-class operation.
- Overlay history, overlay input, menus, and the main buffer are fused into a
  single frame model, which makes partial updates fragile.
- Wheel storms and overscroll bursts create repeated parse/redraw churn because
  the renderer lacks a semantic notion of "this region scrolled" versus "these
  rows happened to move".
- The system is optimized around terminal escape generation, not around a
  stable authoritative screen model.

The result is that local fixes tend to be narrow, timing-sensitive, and easy to
regress.

## Lessons From Neovim

Neovim uses a better split:

- Persistent screen grids are the source of truth.
- Rendering mutates those grids incrementally.
- Grid updates are compared against existing cell state while drawing, not
  after a full-screen snapshot is built.
- Scroll is a semantic operation.
- Floating windows, messages, and other overlays are composited as separate
  grids.
- UI backends consume a compact stream of semantic operations and flush at
  explicit boundaries.

The important pieces to copy are:

### 1. Persistent Grid State

Neovim's `ScreenGrid` stores:

- cells
- attributes
- row offsets
- validity state
- compositor metadata

The key property is that the current screen model is persistent and
authoritative. Rendering compares against it directly.

### 2. Line-Oriented Incremental Drawing

Neovim renders a line through a small API:

- `grid_line_start`
- `grid_line_put_*`
- `grid_line_clear_end`
- `grid_line_flush`

That API allows:

- building one line at a time
- tracking the exact dirty span
- comparing against the existing grid while drawing
- emitting only the minimum operation needed

### 3. Semantic Scroll Operations

Neovim does not rediscover scroll by diffing whole screens. It has explicit
grid scroll operations, and the UI contract says the exposed area will be
filled immediately afterward.

That is a much better fit for:

- buffer viewport movement
- HUD history scrolling
- menu scrolling

### 4. Separate Compositor

Neovim keeps composition separate from drawing. Distinct grids are layered and
composed into the final display.

That is directly relevant to Ion because the current renderer struggles most
when buffer, HUD, and menu interact.

### 5. Explicit Flush Boundaries And Back-Pressure

Neovim batches redraw events and flushes intentionally. The backend also has
explicit event-queue back-pressure handling.

Ion needs the same mindset:

- gather semantic updates
- flush once per redraw boundary
- avoid unbounded event/render churn

## Target Architecture For Ion

Ion should move to a grid-based renderer with a compositor.

### Core Concepts

- `ScreenGrid`
  - Persistent cell storage for one logical surface.
- `GridCell`
  - Rune or glyph payload plus style id.
- `GridLineBuilder`
  - Scratch builder for one output row before commit.
- `Compositor`
  - Merges multiple logical grids into the final terminal surface.
- `RenderBackend`
  - Emits terminal escape sequences for semantic operations.
- `RenderScheduler`
  - Coalesces invalidations and flushes redraw work at controlled boundaries.

### Proposed Grids

- `bufferGrid`
  - Main text viewport only.
- `hudHistoryGrid`
  - Scrollable command/output history region.
- `hudInputGrid`
  - Prompt and input row.
- `menuGrid`
  - Context menu / popup content.
- `rootGrid`
  - Final composed terminal-sized grid.

We should not keep a single fused "frame" as the main abstraction.

## Data Model

### ScreenGrid

Each grid should store:

- `rows`, `cols`
- `cells []GridCell`
- `lineOffset []int`
- `valid bool`
- `zindex int`
- `originRow`, `originCol`
- `visible bool`
- `dirtyRows` or dirty spans

Optional later additions:

- blending
- mouse enablement
- per-row dirty extent

### GridCell

Each cell should store:

- glyph
- style id

Initially we can keep style as an interned string or compact style struct, but
the long-term design should use style ids instead of raw ANSI strings in every
cell.

## Rendering Pipeline

### Old Model

- state change
- build entire frame
- diff frames
- emit terminal sequences

### New Model

1. State change marks one or more grids/regions dirty.
2. Renderer updates the affected grid line-by-line.
3. Each line flush compares against the grid's existing cell state.
4. The renderer emits semantic operations:
   - line update
   - clear to end
   - scroll region
   - cursor move
   - grid resize
5. The compositor merges visible grids into `rootGrid`.
6. The backend flushes terminal output once per redraw boundary.

## Scrolling Model

Scrolling must become explicit.

### Buffer Viewport

When the main viewport scrolls:

- update buffer model origin
- rotate row offsets or otherwise perform a semantic grid scroll
- mark only exposed rows dirty
- redraw exposed rows

Do not re-derive scrolling by comparing two fully rendered full-screen
snapshots.

### HUD History

HUD history should use the same mechanism:

- semantic region scroll
- redraw only exposed rows

The current HUD/buffer split should not imply separate ad hoc redraw logic.

## Composition Model

Composition should be explicit and layered.

### Layering

Suggested order:

1. buffer grid
2. HUD history grid
3. HUD input grid
4. menu grid

The compositor should own:

- placement
- clipping
- overlap resolution
- future blending support if needed

### Benefits

- HUD no longer forces full-screen frame rebuild semantics.
- Menu rendering becomes naturally isolated.
- Mouse hit testing can be mapped to grid ownership instead of inferred from
  raw terminal rows.

## Backend Contract

The backend should consume semantic operations, not full frames.

### Minimum Operation Set

- `resize`
- `scrollRegion`
- `writeCells`
- `clearRegion`
- `cursorGoto`
- `setCursorStyle`
- `setModes`
- `flush`

The terminal backend may still internally optimize these operations further,
but the renderer should not be written around raw ANSI string diffs.

## Invalidations

Ion should move from redraw classes toward invalidation regions and operation
types.

Instead of asking "is this `redrawOverlayHistory` or `redrawBufferViewport`?",
the system should ask:

- which grid changed?
- which rows or spans changed?
- did a semantic scroll happen?
- did composition change?
- did terminal mode or cursor state change?

This does not mean redraw classes must disappear immediately, but they should
stop being the primary renderer abstraction.

## Event Coalescing And Back-Pressure

The current freeze work shows that input bursts and rendering bursts need to be
handled separately.

### Principles

- Coalesce repeated wheel input before render work.
- Coalesce render invalidations before terminal flush.
- Avoid one terminal flush per input packet.
- Add explicit back-pressure protection when redraw work outpaces terminal
  output.

This should be part of the renderer design, not a later patch.

## Non-Goals

This redesign should copy Neovim's architecture, not all of its surface area.

Not required in phase 1:

- msgpack-rpc UI protocol
- external windows
- full multigrid remote UI support
- Neovim's full highlight metadata model
- complete TUI compatibility machinery

## Migration Plan

### Phase 0: Design Lock

- Commit to the grid/compositor architecture.
- Stop adding more heuristic frame-diff features unless needed as short-term
  safety fixes.
- Treat the current `terminalFrame` pipeline as transitional.

### Phase 1: Introduce Core Types

- Add `ScreenGrid`.
- Add `GridCell`.
- Add `GridLineBuilder`.
- Add a minimal style-id representation.
- Add tests for:
  - line writes
  - clear to end
  - double-width behavior if relevant
  - row-offset scrolling

Deliverable:

- new grid package or module with no terminal backend dependency

### Phase 2: Build A Terminal Backend Around Semantic Ops

- Add a backend interface for semantic render operations.
- Implement a TTY backend for:
  - write cells
  - clear region
  - scroll region
  - cursor updates
  - flush

Deliverable:

- backend that can drive a single root grid

### Phase 3: Route Buffer Viewport Through The New Grid

- Render only the main buffer viewport through `bufferGrid`.
- Use semantic scroll for viewport motion.
- Keep HUD/menu on the old path temporarily if needed.

Deliverable:

- main scrolling path no longer depends on `terminalFrame` diffing

This is the first major checkpoint. If this is not solid, stop and fix it
before migrating overlays.

### Phase 4: Add Compositor And Split Overlay Surfaces

- Introduce `hudHistoryGrid`, `hudInputGrid`, and `menuGrid`.
- Add compositor that merges visible grids into `rootGrid`.
- Move HUD rendering to explicit layered composition.

Deliverable:

- HUD and menu no longer trigger special-case full-frame behavior

### Phase 5: Remove Frame-Diff-Centric Logic

- Delete or heavily reduce `terminalFrame` snapshot diff logic.
- Delete row-shift heuristics that exist only because scrolling was inferred
  too late.
- Simplify redraw classification into invalidation scheduling.

Deliverable:

- renderer is grid-first, not frame-diff-first

### Phase 6: Stabilization

- stress tests for wheel storms
- stress tests for repeated resize
- stress tests for HUD open/close while scrolling
- trace instrumentation for invalidation, scroll, compose, and flush stages

## Concrete Initial File Plan

One reasonable implementation layout:

- `internal/client/term/grid.go`
  - `ScreenGrid`, row offsets, invalidation tracking
- `internal/client/term/grid_line.go`
  - line builder and line flush logic
- `internal/client/term/compositor.go`
  - layering and root composition
- `internal/client/term/render_backend.go`
  - semantic backend interface
- `internal/client/term/render_tty.go`
  - ANSI terminal backend
- `internal/client/term/render_scheduler.go`
  - invalidation batching and flush scheduling

Likely transitional edits:

- `internal/client/term/term.go`
  - switch redraw entry points to invalidation + flush
- `internal/client/term/frame.go`
  - keep temporarily, then retire

## Recommended First Implementation Slice

The first slice should be narrow:

1. Add `ScreenGrid` and line flush logic.
2. Render the main buffer viewport into `bufferGrid`.
3. Support semantic vertical scrolling for the buffer viewport.
4. Keep HUD and menu on the old renderer for one transition step.

This gives the fastest path to testing the architecture on the highest-volume
path: normal scrolling.

## Decision

Ion should adopt Neovim's renderer architecture in substance:

- persistent grids
- semantic scroll
- separate compositor
- explicit flush boundaries
- backend-driven terminal output

We should not continue investing in the current whole-frame diff model beyond
what is needed to keep the editor usable during the rewrite.
