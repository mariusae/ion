# Ion Architecture

Ion is a terminal-based text editor compatible with the classic Unix
[sam](http://doc.cat-v.org/plan_9/4th_edition/papers/sam/) editor. It is
structured as a **client-server system** communicating over a custom binary
wire protocol.

## Overview

```
┌──────────────────┐       Unix socket        ┌──────────────────────────────┐
│  Client          │ ◄──── ION1 frames ─────► │  Server                      │
│  (term or        │                           │                              │
│   download mode) │                           │  ┌────────────────────────┐  │
│                  │                           │  │ transport.Server       │  │
│  session.Client  │                           │  │  ┌──────────────────┐  │  │
│  (wire adapter)  │                           │  │  │ session.TermSess │  │  │
│                  │                           │  │  └──────────────────┘  │  │
│                  │                           │  │  ┌──────────────────┐  │  │
│                  │                           │  │  │ workspace.       │  │  │
│                  │                           │  │  │  Workspace (mu)  │  │  │
│                  │                           │  │  └──────────────────┘  │  │
│                  │                           │  │  ┌──────────────────┐  │  │
│                  │                           │  │  │ exec.Session     │  │  │
│                  │                           │  │  │ (core sam engine)│  │  │
│                  │                           │  │  └──────────────────┘  │  │
│                  │                           │  └────────────────────────┘  │
└──────────────────┘                           └──────────────────────────────┘
```

The server owns all editing state. Clients are thin: they send requests
(edit, navigate, save) and receive back complete buffer snapshots. There is
no client-side document model, CRDT, or operational transform — the server
is the single source of truth, protected by a mutex.

## Transport

Clients and the server communicate over a **Unix domain socket** (path
`/tmp/ion-*.sock`). The server listens with `net.Listen("unix", path)`;
clients connect with `net.Dial("unix", path)`. Each accepted connection
spawns a goroutine (`ServeConn`) that reads frames in a loop.

## Wire Protocol (ION1)

Every message is wrapped in a fixed-size **28-byte frame header** followed
by a variable-length payload:

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|      'I'      |      'O'      |      'N'      |      '1'      |  Magic
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          Version (1)          |          Kind (uint16)         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|            Flags              |           Reserved            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         Request ID                            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                                               |
|                         Session ID                            |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Payload Length                            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                      Payload (variable)                       |
|                           ...                                 |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

All multi-byte integers are **little-endian**. Strings are length-prefixed
(uint32 byte count followed by UTF-8 data). The maximum payload is 64 MiB.

**Request ID** correlates a response (or stream of events) back to the
request that triggered it. The client increments a counter for each new
request.

**Session ID** identifies the client connection. The server assigns it on
the first response; the client learns it from the reply and echoes it in
subsequent requests.

## Message Types

Ion defines 21 message kinds (see `internal/proto/wire/codec.go`). They fall
into four groups:

### Session lifecycle

| Kind | Name | Direction | Payload | Response |
|------|------|-----------|---------|----------|
| 1 | `BootstrapRequest` | client → server | `[]string` file paths | `OKResponse` |
| 2 | `OpenFilesRequest` | client → server | `[]string` file paths | `BufferViewMessage` |

`BootstrapRequest` is sent once at session start to load the initial file
set. `OpenFilesRequest` is used later to open additional files.

### Command execution

| Kind | Name | Direction | Payload | Response |
|------|------|-----------|---------|----------|
| 4 | `CommandRequest` | client → server | sam command script (string) | `CommandResponse` |
| 5 | `CommandResponse` | server → client | `Continue` (bool) | — |

The download-mode client sends sam commands as strings. The server parses
and executes them, streaming any output back as `StdoutEvent`/`StderrEvent`
frames before the final `CommandResponse`.

### Editing operations

| Kind | Name | Direction | Payload | Response |
|------|------|-----------|---------|----------|
| 9 | `CurrentViewRequest` | client → server | (empty) | `BufferViewMessage` |
| 13 | `FocusRequest` | client → server | file ID (int) | `BufferViewMessage` |
| 14 | `AddressRequest` | client → server | sam address expression (string) | `BufferViewMessage` |
| 15 | `SetDotRequest` | client → server | start, end (int, int) | `BufferViewMessage` |
| 16 | `ReplaceRequest` | client → server | start, end (int, int), text (string) | `BufferViewMessage` |
| 17 | `UndoRequest` | client → server | (empty) | `BufferViewMessage` |
| 18 | `SaveRequest` | client → server | (empty) | `SaveResponse` |

All editing requests return a `BufferViewMessage` — a full snapshot of the
current buffer after the operation. This is the key design choice: the
server always sends back the **complete, authoritative state** so the client
never needs to maintain its own copy.

The `BufferViewMessage` contains:

```go
type BufferView struct {
    ID       int    // stable file identifier
    Text     string // full buffer contents
    Name     string // file name/path
    DotStart int    // selection start (rune offset)
    DotEnd   int    // selection end (rune offset)
}
```

### File menu

| Kind | Name | Direction | Payload |
|------|------|-----------|---------|
| 11 | `MenuFilesRequest` | client → server | (empty) |
| 12 | `MenuFilesResponse` | server → client | `[]MenuFile` |

Each `MenuFile` contains `{ID, Name, Dirty, Current}` — enough for the
terminal UI to render the file picker.

### Streaming output and errors

| Kind | Name | Direction | Payload |
|------|------|-----------|---------|
| 7 | `StdoutEvent` | server → client | data chunk (string) |
| 8 | `StderrEvent` | server → client | data chunk (string) |
| 3 | `OKResponse` | server → client | (empty) |
| 6 | `ErrorResponse` | server → client | message + diagnostic (strings) |

Output events are streamed **mid-request**: the server writes them to the
connection while a command is still running. They carry the same
`RequestID` as the triggering request. The client's `roundTrip` loop
collects and dispatches them before the final response arrives.

### Broadcast events (reserved)

| Kind | Name | Direction |
|------|------|-----------|
| 20 | `BufferUpdateEvent` | server → client |
| 21 | `MenuUpdateEvent` | server → client |

These are defined but not yet used. They exist to support future
multi-client broadcast when the server pushes state changes to other
connected clients.

## Example: Client Edits a File

Suppose a file `hello.txt` contains `Hello world` (11 runes), the cursor
(dot) is at position 5, and the user types `, dear` to insert text at the
cursor. Here is the complete message flow:

### 1. Client sends `ReplaceRequest`

The client calls `Replace(5, 5, ", dear")` — insert at position 5 with an
empty selection (start == end means pure insertion).

```
Frame header:
  Magic:     "ION1"
  Version:   1
  Kind:      16 (KindReplaceRequest)
  Flags:     0
  RequestID: 7          (auto-incremented)
  SessionID: 1
  Payload:   [00 00 00 05]                  // Start = 5
             [00 00 00 05]                  // End   = 5
             [00 00 00 06] 2c 20 64 65 61 72 // Text  = ", dear"
```

### 2. Server processes the edit

The transport layer decodes the frame, dispatches to `handleFrame`, which
calls:

```go
// transport/server.go
case *wire.ReplaceRequest:
    view, err := session.Replace(msg.Start, msg.End, msg.Text)
```

This flows through the session into the mutex-protected workspace:

```go
// workspace/workspace.go
func (w *Workspace) Replace(start, end int, repl string) (wire.BufferView, error) {
    w.mu.Lock()
    defer w.mu.Unlock()
    if err := w.session.ReplaceCurrent(text.Posn(start), text.Posn(end), repl); err != nil {
        return wire.BufferView{}, err
    }
    return w.currentView()
}
```

The core `exec.Session` inserts `", dear"` at position 5 in the file's
buffer, logs the operation to the undo history (epsilon buffer), and marks
the file dirty.

### 3. Server sends `BufferViewMessage`

```
Frame header:
  Magic:     "ION1"
  Version:   1
  Kind:      10 (KindCurrentViewResponse)
  Flags:     0
  RequestID: 7          (same as request)
  SessionID: 1
  Payload:
    ID:       1
    Text:     "Hello, dear world"
    Name:     "hello.txt"
    DotStart: 11         (cursor after inserted text)
    DotEnd:   11
```

### 4. Client updates the UI

The client's `roundTrip` returns the `BufferViewMessage`. The terminal
renderer uses the new `Text`, `DotStart`, and `DotEnd` to redraw the
buffer.

## Example: Command Execution with Streaming Output

When a download-mode client sends the sam command `1,2p` (print lines 1-2):

```
Client → Server:  CommandRequest{Script: "1,2p"}     (Kind 4, ReqID 3)
Server → Client:  StdoutEvent{Data: "Hello, dear world\n"}  (Kind 7, ReqID 3)
Server → Client:  CommandResponse{Continue: true}     (Kind 5, ReqID 3)
```

The `StdoutEvent` is streamed mid-request. The client's `roundTrip` loop
writes the data to stdout and continues reading until the final
`CommandResponse` arrives.

## Example: Opening a File and Navigating

```
Client → Server:  BootstrapRequest{Files: ["main.go", "util.go"]}
Server → Client:  OKResponse{}

Client → Server:  CurrentViewRequest{}
Server → Client:  BufferViewMessage{ID: 1, Name: "main.go", Text: "package main\n...", DotStart: 0, DotEnd: 0}

Client → Server:  FocusRequest{ID: 2}
Server → Client:  BufferViewMessage{ID: 2, Name: "util.go", Text: "package util\n...", DotStart: 0, DotEnd: 0}

Client → Server:  AddressRequest{Expr: "/func /"}
Server → Client:  BufferViewMessage{ID: 2, Name: "util.go", Text: "...", DotStart: 45, DotEnd: 50}
```

## Server Layers

The server is organized in three layers, each adding a concern:

1. **`transport.Server`** — Accepts connections, reads/writes ION1 frames,
   dispatches to sessions. Owns the `eventWriter` that converts Go
   `io.Writer` calls into `StdoutEvent`/`StderrEvent` frames.

2. **`session.TermSession`** — Per-client facade. Holds the session ID,
   stdout/stderr writers. Delegates all operations to the shared workspace.

3. **`workspace.Workspace`** — Mutex-protected wrapper around the core
   `exec.Session`. Serializes all edits from all clients. Converts between
   wire types (`wire.BufferView`) and core types (`text.Posn`).

4. **`exec.Session`** / **`text.File`** — The sam-compatible core. Manages
   file buffers (disk-backed rune storage), undo logs (delta/epsilon
   buffers), the sam command language parser, and address evaluation.

## Module Boundaries

Enforced by `architecture_test.go`:

- **`core/`** has no dependencies on `server/`, `client/`, or `cmd/`
- **`server/`** has no dependencies on `client/`
- **`client/`** has no dependencies on `server/`

This keeps the core editing engine reusable and the client/server cleanly
separated.

## Concurrency Model

There is no CRDT, OT, or distributed consensus. The workspace mutex
serializes all operations. Multiple clients can connect to the same server,
but they share one editing session — all clients see the same files, the
same cursor, and the same undo history. This is intentional: ion is modeled
after sam's split-process architecture where the server is authoritative and
the client is a view.
