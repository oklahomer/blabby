# blabby

A distributed chat application built with Go and Proto.Actor virtual actors.

## Project Structure

```
blabby/
├── buf.yaml                 # buf v2 workspace config
├── buf.gen.yaml             # Protobuf code generation config
├── cmd/
│   ├── server/              # Chat server entry point
│   └── client/              # TUI client entry point
├── internal/
│   ├── grain/
│   │   ├── user/            # User virtual actor (grain)
│   │   └── room/            # Room virtual actor (grain)
│   ├── actor/
│   │   └── connection/      # WebSocket connection actor
│   ├── gateway/             # HTTP/WebSocket gateway
│   ├── auth/                # Authentication (JWT)
│   ├── persistence/         # Storage backends (Phase 2)
│   ├── middleware/           # HTTP middleware
│   └── testutil/
│       ├── grain/           # Grain test helpers
│       └── cluster/         # Test cluster bootstrap
├── proto/                   # Protobuf service definitions
│   ├── room/room.proto      # Room grain: Join, Leave, PostMessage
│   └── user/user.proto      # User grain: connection, routing, events, queries
├── gen/                     # Generated Go code (committed)
│   ├── room/                # Room grain messages + client (package roompb)
│   └── user/                # User grain messages + client (package userpb)
├── api/                     # OpenAPI and AsyncAPI specs
└── docs/
    └── adr/                 # Architecture Decision Records
```

## Code Generation

Blabby uses [buf](https://buf.build/) to orchestrate protobuf code generation with two plugins:

1. **`buf.build/protocolbuffers/go`** generates Go structs for protobuf messages (`.pb.go` files).
2. **`protoc-gen-go-grain`** generates Proto.Actor grain interfaces, clients, and actor wrappers (`_grain.pb.go` files) from `service` definitions.

The generated code in `gen/` is committed to the repository so developers can clone and build without running code generation locally.

### Prerequisites

To regenerate protobuf code after modifying `.proto` files:

- [buf](https://buf.build/docs/installation/) CLI
- `protoc-gen-go-grain`:
  ```bash
  go install github.com/asynkron/protoactor-go/protobuf/protoc-gen-go-grain@latest
  ```

### Regenerating

```bash
buf generate
```

The output in `gen/` should be identical each run. Verify with:

```bash
buf generate && git diff --exit-code gen/
```
