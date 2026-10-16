//go:build pgx

package main

// Built only with -tags pgx, after: go get github.com/jackc/pgx/v5
import _ "github.com/jackc/pgx/v5/stdlib"

func init() { sqlDriver = "pgx" }
