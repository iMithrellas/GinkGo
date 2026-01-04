package db

import (
	"context"
	"database/sql"
)

type txKey struct{}

// WithTx stores a transaction in the context for repository methods to reuse.
func WithTx(ctx context.Context, tx *sql.Tx) context.Context {
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFromContext returns a transaction from context when available.
func TxFromContext(ctx context.Context) *sql.Tx {
	if ctx == nil {
		return nil
	}
	tx, _ := ctx.Value(txKey{}).(*sql.Tx)
	return tx
}

type TxProvider interface {
	BeginTx(ctx context.Context) (*sql.Tx, error)
}
