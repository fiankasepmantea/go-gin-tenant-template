package grpc

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func plain() credentials.TransportCredentials { return insecure.NewCredentials() }

func codeOf(err error) codes.Code { return status.Code(err) }
