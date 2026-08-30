// Package dbtx names the shared PostgreSQL transaction handle so a single
// atomic fact can span two module-owned tables without either module
// importing the other: each module's adapter writes only its own tables
// inside the transaction, and the composition layer owns the boundary.
package dbtx

import "github.com/jackc/pgx/v5"

// Tx is the shared transaction handle (pgx.Tx).
type Tx = pgx.Tx
