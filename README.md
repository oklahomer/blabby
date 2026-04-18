# blabby

A distributed chat application built with Go and Proto.Actor virtual actors.

## Project Structure

```
blabby/
├── cmd/
│   ├── server/          # Chat server entry point
│   └── client/          # TUI client entry point
├── internal/
│   ├── grain/
│   │   ├── user/        # User virtual actor (grain)
│   │   └── room/        # Room virtual actor (grain)
│   ├── actor/
│   │   └── connection/  # WebSocket connection actor
│   ├── gateway/         # HTTP/WebSocket gateway
│   ├── auth/            # Authentication (JWT)
│   ├── persistence/     # Storage backends (Phase 2)
│   ├── middleware/       # HTTP middleware
│   └── testutil/
│       ├── grain/       # Grain test helpers
│       └── cluster/     # Test cluster bootstrap
├── proto/               # Protobuf definitions
├── gen/go/              # Generated Go code from protobuf
├── api/                 # OpenAPI and AsyncAPI specs
└── docs/
    └── adr/             # Architecture Decision Records
```
