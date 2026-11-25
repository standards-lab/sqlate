package pgvector_test

import (
	"slices"
	"testing"

	"github.com/standards-lab/sqlate/postgres/pgvector"
)

func TestMetrics_ReturnsL2CosineAndInnerProductInOrder(t *testing.T) {
	want := []pgvector.Metric{pgvector.L2, pgvector.Cosine, pgvector.InnerProduct}
	if got := pgvector.Metrics(); !slices.Equal(got, want) {
		t.Fatalf("Metrics() = %v, want %v", got, want)
	}
}

func TestMetrics_ReturnsASliceTheCallerOwns(t *testing.T) {
	pgvector.Metrics()[0].Operator = "changed"
	if got := pgvector.Metrics()[0]; got != pgvector.L2 {
		t.Fatalf("Metrics()[0] = %v after a caller's write, want %v", got, pgvector.L2)
	}
}
