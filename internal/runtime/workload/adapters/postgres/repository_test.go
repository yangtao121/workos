package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestReserveConflictClassificationUsesOnlyUniqueViolationSQLState(t *testing.T) {
	t.Parallel()
	unique := &pgconn.PgError{Code: "23505", Message: "synthetic"}
	if !isUniqueViolation(fmt.Errorf("insert workload: %w", unique)) {
		t.Fatal("wrapped unique violation was not classified as a reserve race")
	}
	for _, err := range []error{
		&pgconn.PgError{Code: "23514", Message: "check violation"},
		&pgconn.PgError{Code: "23503", Message: "foreign key violation"},
		errors.New("driver programming failure"),
	} {
		if isUniqueViolation(err) {
			t.Fatalf("non-unique failure was collapsed to a reserve race: %v", err)
		}
	}
}
