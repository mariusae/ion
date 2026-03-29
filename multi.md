# Multi-Client Workspace Design

## Goal

Allow multiple clients to share one workspace while keeping the model small and
regular.

The design should have:

- one shared workspace
- sessions owned by clients
- temporary take/return, but no arbitrary session reassignment
- one active lease per file
- one command language
- one execution primitive per session
- one extension mechanism
- out-of-process extensions only

## Core Model

### Workspace

The workspace owns:

- the shared file set
- shared file contents
- file leases
- the client registry
- the session registry
- the namespace registry

### Client

A client is a transport endpoint.

A client owns zero or more sessions.

The client also owns client-local presentation state, including:

- HUD / transcript
- scroll origin
- menu state
- overlay state
- screen cursor position

Most interactive editor clients create one or more sessions they own. Service
clients such as LSP providers do not create home sessions of their own; they
only take and return sessions owned by other clients.

If a client dies, all sessions it owns die with it.

### Session

A session is the live editing context.

Each session has:

- an immutable owner client
- a current controller, which is either the owner or a temporary taker

A session is never reassigned to a different owner. The only control transfer
is:

- owner -> taker, via take
- taker -> owner, via return

If a session is returned after its owner has died, the session is torn down.

The session owns:

- current working directory
- current file lease
- navigation stack
- quit state
- shell-repeat state
- command state and cancellation state

Presentation details such as the HUD / transcript, scroll origin, menu state,
overlay state, and screen cursor position remain client-local UI state.

### File

Each file owns:

- shared text contents
- `dot`
- `mark`

`dot` follows the file, not the session.

That is unambiguous because a file may be active in at most one session at a
time.

### File Leases

Each file has zero or one active session.

Each session has zero or one current file.

Changing the current file transfers the lease.

Taking a session also takes whatever file lease that session currently owns.
If the taken session navigates to another file, the lease moves by ordinary
commands. When the session returns, it returns with whatever file it currently
owns.

This gives the provider a strong guarantee: while it controls the taken
session, the active file and its `dot`/`mark` are live, local, and cannot be
silently displaced by another client.

## API Shape

The transport API should be client/session-oriented.

Sketch:

```go
client := client.Dial(...)

sess := client.NewSession()

// Blocks if this client does not currently control sess.
err := sess.Execute("B foo.go:123", stdout, stderr)

// Always cancels the current operation for sess, regardless of who currently
// controls it.
err := sess.Cancel()
```

That means:

- sessions are explicit objects
- sessions are created by their owner client
- `Execute` is untargeted in the sense that it executes against `sess`
- `Execute` requires current control of `sess`
- `Execute` writes command output to client-provided writers
- `Cancel` is owner-authoritative and does not require current control

Additional required session control operations are:

```go
sess := client.NewSession()
sessions := client.Sessions()

err := client.Take(sess.ID())
err := client.Return(sess.ID())
```

The exact naming is flexible. The important part is the ownership model:

- create your own sessions
- temporarily take someone else's session
- always return it to its owner
- never rehome it

## Command Model

### One Command Language

Built-ins and extensions use the same parser and the same execution model.

The canonical namespaced form is:

- `:ion:B`
- `:sess:list`
- `:sess:take`
- `:sess:return`
- `:lsp:goto`

Legacy built-in syntax may remain as sugar:

- `B foo.go:123` == `:ion:B foo.go:123`
- `p` == `:ion:p`

Extension commands must be legal anywhere a normal command is legal.

Example:

- `X/./:lsp:info`

There is no second command language for extensions.

### Session Commands vs Session API

There are two layers:

- the transport API, where a client creates sessions it owns and takes/returns
  sessions
- the command language, which runs inside the currently controlled session

So:

- `sess.Execute("B foo.go:123")` is the transport-side way to run an ordinary
  command
- `:sess:list`, `:sess:take`, and `:sess:return` may still exist as ordinary
  commands for interactive use and scripting

The ownership and blocking semantics come from the session API, not from a
separate targeted-execute mechanism.

### Serial Per Session

Each session may have at most one active command.

A session is always in one of a small number of states:

- idle
- running locally
- taken
- delegated
- canceling

`sess.Execute(script, stdout, stderr)` means:

- run `script` against `sess`
- if the calling client currently controls `sess`, start immediately when idle
- otherwise block until the client regains control of `sess`

Only one command runs at a time in a session. There is no per-session queue.

`sess.Cancel()` applies to the currently running command for `sess`, regardless
of who currently controls it.

This is the whole synchrony model.

### Context

A command's context is the live session it is running in, not a copied
snapshot.

At any instant, command context consists of:

- session cwd
- session current file lease
- the current file's `dot`
- the current file's `mark`
- session navigation stack
- session cancellation state

Built-in commands operate on that context directly.

Extensions get the same context by temporary control of the same live session.

### Output

Output belongs to the calling client, not the session.

A command runs against a live session, but its stdout/stderr are supplied by the
current caller of `sess.Execute(...)`.

In UI terms, the HUD is a client concept, not a session concept.

That means:

- session state changes are persistent
- command output is not persistent session state
- the client currently issuing the command receives the output live in its HUD
- delegated extension commands write back to the original calling client, just
  like built-in commands

If control later returns to the owner, the owner sees the changed session
state, but not a replayed HUD / transcript from someone else's earlier call.

## Extension Model

Extensions run in their own binaries as ordinary clients.

The server never loads extension code in-process.

### Namespace Registration

Namespaces are exclusive.

At most one client may register `lsp` at a time.

Registration only extends command lookup. It does not create a second dispatch
mechanism.

### Service Clients

Service clients do not need home sessions.

Their job is:

- register namespaces
- receive invocation requests
- take target sessions
- run ordinary session commands there
- return the sessions

They may own zero sessions of their own.

### Delegated Session Execution

When a session reaches `:namespace:command` during ordinary evaluation:

1. command lookup resolves the namespace to a provider client
2. the server tells the provider which session needs service
3. the provider takes that session
4. the provider runs ordinary `sess.Execute(...)` calls against it
5. when finished, the provider returns the session to its owner

That is the entire extension mechanism.

There is no targeted execute API. There is no need for service-owned editor
sessions. While delegated, the provider simply controls the same live session
the built-in command would have run in.

## Provider Protocol

The provider protocol should stay minimal.

### Server to Provider

```go
type Invoke struct {
    InvocationID string
    SessionID    string
    Script       string
}

type InvocationCanceled struct {
    InvocationID string
}
```

`Invoke` means:

- this invocation corresponds to the extension command named by `Script`
- session `SessionID` is the owner-backed session you must take
- use ordinary take/execute/return operations on that session

### Provider to Server

```go
type FinishInvocation struct {
    InvocationID string
    Err          string
}
```

Success is `Err == ""`.

Failure is an ordinary command failure with a message in `Err`.

No other extension-specific execution messages are required.

## Cancellation

Cancellation is gentle and cooperative.

The goal is not to forcibly unwind arbitrary work. The goal is to notify the
currently running command that it should stop and fail normally.

### Top-Level

- `sess.Cancel()` cancels the currently running command in `sess`
- this is allowed even when another client currently controls `sess`

If no command is running, `Cancel` is a no-op.

### Built-Ins

Built-ins should observe normal session cancellation and fail accordingly.

For shell commands such as `!`, cancellation should map to existing control-C
style behavior as closely as practical.

### Extensions

If the running command is delegated, the server sends `InvocationCanceled` to
the provider.

A well-behaved provider should:

1. stop its work
2. allow any in-flight session command to finish or fail naturally
3. return the session with `:sess:return`
4. finish the invocation with a normal failure

Cancellation does not introduce a second failure model.

## `ion -B` Example

`ion -B foo.go:123` should use the same model as everything else.

The flow is:

1. connect an ephemeral client to the resident server
2. run `:sess:list`
3. choose the most recently active editor session
4. run `:sess:take <session-id>`
5. run `sess.Execute("B foo.go:123")`
6. run `:sess:return`
7. disconnect

That keeps `-B` inside the same execution model as ordinary interactive use.

Its output, if any, belongs to the ephemeral calling client that issued `-B`,
not to the borrowed session owner.

## LSP Example

Assume:

- client 1 is an LSP integration client
- client 2 is an editor client
- client 1 registers namespace `lsp`
- client 2 owns session `s2`

### Step 1

Client 2's session is at:

- cwd: `/repo`
- file: `pkg/foo.go`
- `dot`: the symbol under the cursor

### Step 2

Inside `s2`, client 2 executes:

- `:lsp:goto`

### Step 3

Normal evaluation reaches `:lsp:goto`. The workspace resolves `lsp` to client 1
and sends:

- `Invoke{SessionID: s2, Script: ":lsp:goto"}`

### Step 4

Client 1 takes `s2`.

### Step 5

Client 1 now operates on `s2` exactly as if it were local:

- it reads the current file and `dot`
- it asks the language server for the definition

### Step 6

The language server resolves the definition to:

- `pkg/bar.go:123`

### Step 7

Client 1 runs an ordinary command on `s2`:

- `s2.Execute(":ion:B pkg/bar.go:123")`

That command releases the old file lease, acquires the new one, and places
`dot` at the target location in `pkg/bar.go`.

### Step 8

Client 1 returns `s2` and finishes the invocation.

### Result

Control of `s2` returns to client 2.

Client 2 is now editing `pkg/bar.go` at the definition site.

From client 2's point of view, `:lsp:goto` behaved like an ordinary command,
because it was one ordinary command running in one ordinary session.

Any command output produced during that invocation is delivered to client 2, the
original caller, not to client 1, the service client that temporarily took the
session.

## Migration Plan

### 1. Add Stable Client and Session Identity

- assign stable `ClientID`
- assign stable `SessionID`
- record immutable owner client per session

### 2. Let Clients Own Multiple Sessions

- add `NewSession`
- keep session lifetime tied to owner-client lifetime
- let a client enumerate the sessions it owns or can observe

### 3. Add Temporary Control Transfer

- implement take/return
- enforce that return always goes back to the owner
- tear down a session if its owner is gone at return time

### 4. Move Session State Out of Shared Globals

- make cwd, current file, navigation stack, quit state, and shell-repeat state
  session-owned

### 5. Move `dot` and `mark` Onto Files

- make `dot` and `mark` file-owned
- ensure the active session reads them from its current file

### 6. Enforce File Leases

- allow each file to be active in at most one session
- transfer the lease whenever a session changes current file

### 7. Make Session Execute the Only Execution Primitive

- route all command execution through `sess.Execute(script, stdout, stderr)`
- remove targeted execution from the design

### 8. Add Ordinary Session Commands

- implement `:sess:list`
- implement `:sess:take <session-id>`
- implement `:sess:return`

### 9. Add Namespace Registration

- register exclusive namespaces
- resolve `:namespace:command` through that registry

### 10. Add Delegation on Top of Take/Return

- when an extension command is reached, send `Invoke`
- let the provider take the target session
- let the provider use ordinary `sess.Execute(script, stdout, stderr)`
- return the session on completion

### 11. Add Owner-Authoritative Cancellation

- let `sess.Cancel()` cancel the session regardless of current controller
- notify delegated providers with `InvocationCanceled`

## Summary

The reduced model is:

- shared workspace
- clients own sessions
- a client may own multiple sessions
- service clients may own zero sessions
- sessions can only be temporarily taken and returned
- if the owner dies, its sessions die
- `dot` and `mark` live on files
- one active session per file
- one execution primitive: `sess.Execute(script, stdout, stderr)`
- one cancellation primitive: `sess.Cancel()`
- one extension mechanism: namespace registration
- one extension execution rule: take the live session, run ordinary commands,
  return it to its owner

This is the smallest model I know that still supports:

- multiple clients sharing one workspace
- exclusive active-file ownership
- per-session working directory and navigation
- owner-backed session lifetime
- remote control without targeted execute
- client-scoped command output for both built-ins and delegated commands
- out-of-process extensions
- `ion -B` using the same execution path as everything else
- LSP-style navigation that can return a different file than it started with
