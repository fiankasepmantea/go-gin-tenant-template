# cmd/grpc

Entry point of the gRPC `AuthService` (see [docs/grpc.md](../../docs/grpc.md)).
`main.go` only loads the configuration, wires the dependencies and runs the server.

```bash
make run-grpc          # APP_ENV=development go run ./cmd/grpc, listens on GRPC_ADDR (default :9090)
```
