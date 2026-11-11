// Package grpc is the gRPC transport. It serves AuthService (api/proto/tenantapp/auth/v1) so that other services
// can verify access tokens and ask "may this user call this endpoint?" without holding the PASETO key and without
// reading the database themselves. The decision is made by the same rbac.Service as the REST middleware, so REST
// and gRPC can never disagree.
//
// The generated code is in gen/ (make proto). Run the server with cmd/grpc. See docs/grpc.md.
package grpc
