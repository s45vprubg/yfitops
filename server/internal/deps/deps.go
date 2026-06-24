// Package deps pins external dependencies in go.mod before the parallel build
// fan-out, so build agents working in separate packages don't race on go.mod /
// go.sum via concurrent `go mod tidy`. It is otherwise unused and can be
// deleted once every dependency has a real importer.
package deps

import (
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/quic-go/quic-go"
	_ "github.com/quic-go/quic-go/http3"
	_ "github.com/quic-go/webtransport-go"
	_ "github.com/redis/go-redis/v9"
)
