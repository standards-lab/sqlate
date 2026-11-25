package pgvector

// Extension is pgvector's name in the catalog.
const Extension = "vector"

// Metric is one distance metric over the vector type: Name is the metric's
// name, Operator the distance operator that orders by it, and OpClass the
// index operator class an HNSW or IVFFlat index builds for that operator.
type Metric struct {
	Name     string
	Operator string
	OpClass  string
}

var (
	// L2 is Euclidean distance.
	L2 = Metric{Name: "l2", Operator: "<->", OpClass: "vector_l2_ops"}
	// Cosine is cosine distance.
	Cosine = Metric{Name: "cosine", Operator: "<=>", OpClass: "vector_cosine_ops"}
	// InnerProduct is the negative inner product.
	InnerProduct = Metric{Name: "inner product", Operator: "<#>", OpClass: "vector_ip_ops"}
)

// Metrics returns the metric table, L2, Cosine, and InnerProduct, in a
// slice the caller owns.
func Metrics() []Metric {
	return []Metric{L2, Cosine, InnerProduct}
}
