# ion-lsp

`ion-lsp` is a delegated `:lsp` namespace provider for `ion`.

It connects to the current ion server over `ION_SOCKET` (or `-socket`), starts
one or more external LSP servers, and routes `:lsp:*` commands to the matching
server for the current buffer.

## Startup

Run `ion-lsp` from the project root you want the language servers to treat as
their workspace root.

Example:

```sh
ion-lsp \
  -server=go:"gopls serve" \
  -server=rust:rust-analyzer \
  -match='\.go$':go \
  -match='\.rs$':rust
```

Notes:

- `-server=name:command` may be repeated.
- `-match=regexp:name` may be repeated.
- match rules choose which configured server handles a file path.
- `rootUri` and `workspaceFolders` are both set to the current working
  directory of `ion-lsp`.

## Commands

After `ion-lsp` registers, these commands are available in ion:

- `:lsp:goto` - jump to definition via `textDocument/definition`
- `:lsp:show` - show hover text via `textDocument/hover`
- `:lsp:gototype` - jump to type definition via `textDocument/typeDefinition`

You can also use:

- `:help :lsp`
- `:help :lsp:goto`
- `:help :lsp:show`
- `:help :lsp:gototype`

## Behavior

- All currently open ion buffers are synced to the matching LSP server.
- Newly opened buffers are discovered and sent with `textDocument/didOpen`.
- Subsequent text changes are sent with `textDocument/didChange`.
- Buffers that disappear from ion are sent with `textDocument/didClose`.
- The LSP client requests `experimental.serverStatusNotification` and
  `window.workDoneProgress`.
- LSP status/progress messages are shown in the most recently active ion
  session as transient status text.

## Go Example

From this repo root:

```sh
ion -N
ion-lsp -server=go:"gopls serve" -match='\.go$':go
```

Then in ion:

- open `cmd/ion/main.go`
- place dot on `runServe`
- run `:lsp:goto`
- run `:lsp:show`
- place dot on a variable such as `cfg`
- run `:lsp:gototype`

## Protocol Notes

`ion-lsp` is generic. It speaks normal LSP JSON-RPC over stdin/stdout with
`Content-Length` framing. There is nothing Go-specific or Rust-specific in the
transport layer beyond the configured server command and file-path match rules.
