package pgvector_test

import (
	"slices"
	"testing"

	"github.com/standards-lab/sqlate/postgres/pgvector"
)

func TestExtension_IsTheCatalogName(t *testing.T) {
	if pgvector.Extension != "vector" {
		t.Fatalf("Extension = %q, want vector", pgvector.Extension)
	}
}

func TestMetrics_MapEachMetricToItsOperatorAndOpClass(t *testing.T) {
	want := []pgvector.Metric{
		{Name: "l2", Operator: "<->", OpClass: "vector_l2_ops"},
		{Name: "cosine", Operator: "<=>", OpClass: "vector_cosine_ops"},
		{Name: "inner product", Operator: "<#>", OpClass: "vector_ip_ops"},
	}
	if got := pgvector.Metrics(); !slices.Equal(got, want) {
		t.Fatalf("Metrics() = %v, want %v", got, want)
	}
	if named := []pgvector.Metric{pgvector.L2, pgvector.Cosine, pgvector.InnerProduct}; !slices.Equal(named, want) {
		t.Fatalf("L2, Cosine, InnerProduct = %v, want %v", named, want)
	}
}

func TestMetrics_ReturnsASliceTheCallerOwns(t *testing.T) {
	pgvector.Metrics()[0].Operator = "changed"
	if got := pgvector.Metrics()[0]; got != pgvector.L2 {
		t.Fatalf("Metrics()[0] = %v after a caller's write, want %v", got, pgvector.L2)
	}
}
