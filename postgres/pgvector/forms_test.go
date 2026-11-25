package pgvector_test

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

var positives = map[string][]string{
	"l2_distance":            {"SELECT id FROM items ORDER BY embedding <-> {{probe}}"},
	"negative_inner_product": {"SELECT id FROM items ORDER BY embedding <#> {{probe}}"},
	"cosine_distance":        {"SELECT id FROM items ORDER BY embedding<=>{{probe}} LIMIT 5"},
	"l1_distance":            {"SELECT id FROM items ORDER BY embedding <+> {{probe}}"},
	"hamming_distance":       {"SELECT id FROM items ORDER BY bits <~> {{probe}}"},
	"jaccard_distance":       {"SELECT id FROM items ORDER BY bits <%> {{probe}}"},

	"vector": {
		"SELECT CAST({{probe}} AS vector)",
		"SELECT CAST({{probe}} AS VECTOR(3))",
		"SELECT CAST({{probe}} AS vector[])",
		"CREATE TABLE items (id bigint, embedding vector(1536))",
		"SELECT id FROM items WHERE embedding = CAST({{probe}} AS vector (3))",
	},
	"halfvec":   {"CREATE TABLE items (embedding halfvec(3))", "SELECT CAST({{probe}} AS HALFVEC)"},
	"sparsevec": {"CREATE TABLE items (embedding sparsevec(5))"},

	"hnsw":    {"CREATE INDEX ON items USING hnsw (embedding vector_l2_ops)", "CREATE INDEX ON items using HNSW(embedding)"},
	"ivfflat": {"CREATE INDEX ON items USING ivfflat (embedding vector_ip_ops) WITH (lists = 100)"},

	"l2_distance()":        {"SELECT l2_distance(a, b) FROM items"},
	"inner_product()":      {"SELECT inner_product (a, b) FROM items"},
	"cosine_distance()":    {"SELECT COSINE_DISTANCE(a, b) FROM items"},
	"l1_distance()":        {"SELECT l1_distance(a, b) FROM items"},
	"hamming_distance()":   {"SELECT hamming_distance(a, b) FROM items"},
	"jaccard_distance()":   {"SELECT jaccard_distance(a, b) FROM items"},
	"vector_dims()":        {"SELECT vector_dims(embedding) FROM items"},
	"vector_norm()":        {"SELECT vector_norm(embedding) FROM items"},
	"l2_norm()":            {"SELECT l2_norm(embedding) FROM items"},
	"l2_normalize()":       {"SELECT l2_normalize(embedding) FROM items"},
	"binary_quantize()":    {"SELECT binary_quantize(embedding) FROM items"},
	"subvector()":          {"SELECT subvector(embedding, 1, 3) FROM items"},
	"array_to_vector()":    {"SELECT array_to_vector({{values}}, 3, true)"},
	"array_to_halfvec()":   {"SELECT array_to_halfvec({{values}}, 3, true)"},
	"array_to_sparsevec()": {"SELECT array_to_sparsevec({{values}}, 5, true)"},

	"hnsw.*":    {"SET hnsw.ef_search = 100", "SET LOCAL hnsw.iterative_scan = relaxed_order"},
	"ivfflat.*": {"SET ivfflat.probes = 10"},
}

var portables = []string{
	"SELECT vector FROM items",
	"SELECT id, vector AS v FROM items WHERE vector IS NOT NULL",
	"SELECT embedding AS vector FROM items",
	"SELECT embedding AS vector, id FROM items",
	"INSERT INTO items (id, vector) VALUES ({{id}}, {{vector}})",
	"INSERT INTO vector (id) VALUES ({{id}})",
	"UPDATE items SET vector = {{vector}} WHERE id = {{id}}",
	"SELECT vector.id FROM items AS vector",
	"SELECT id FROM items WHERE kind = 'vector(3) <=> AS vector USING hnsw' -- l2_distance(a, b)",
	`SELECT "vector(3)", "halfvec" FROM items /* SET hnsw.ef_search = 1 */`,
	"SELECT id FROM items WHERE a <> b AND c <= d AND e >= f AND g < -h ORDER BY id",
	"SELECT count(*) FROM items WHERE a % 2 = 0 AND b ~ 'x' AND c + d > 0",
	"CREATE INDEX items_embedding ON items USING btree (embedding)",
	"SELECT id, subvectors, vectorize, distance FROM items",
}

func forms(t *testing.T) map[string]*regexp.Regexp {
	t.Helper()
	var config struct {
		Export struct {
			NativeForms map[string]string `toml:"native_forms"`
		} `toml:"export"`
	}
	if _, err := toml.DecodeFile("sqlint.toml", &config); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*regexp.Regexp, len(config.Export.NativeForms))
	for name, expr := range config.Export.NativeForms {
		re, err := regexp.Compile(expr)
		if err != nil {
			t.Fatalf("native_forms.%s: %v", name, err)
		}
		out[name] = re
	}
	return out
}

func codeOnly(sql string) string {
	var b strings.Builder
	var quote byte
	inBlock := false
	for i := 0; i < len(sql); i++ {
		switch {
		case inBlock:
			if strings.HasPrefix(sql[i:], "*/") {
				inBlock = false
				i++
			}
		case quote != 0:
			if sql[i] == quote {
				quote = 0
				b.WriteByte(sql[i])
			}
		case sql[i] == '\'' || sql[i] == '"':
			quote = sql[i]
			b.WriteByte(sql[i])
		case strings.HasPrefix(sql[i:], "--"):
			return b.String()
		case strings.HasPrefix(sql[i:], "/*"):
			inBlock = true
			i++
		default:
			b.WriteByte(sql[i])
		}
	}
	return b.String()
}

func matching(forms map[string]*regexp.Regexp, sql string) []string {
	code := codeOnly(sql)
	var names []string
	for name, re := range forms {
		if re.MatchString(code) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func TestForms_AreTheInventory(t *testing.T) {
	got := slices.Sorted(maps.Keys(forms(t)))
	want := slices.Sorted(maps.Keys(positives))
	if !slices.Equal(got, want) {
		t.Fatalf("native_forms names %v, want %v", got, want)
	}
}

func TestForms_EachMatchesItsSampleAlone(t *testing.T) {
	all := forms(t)
	for name, samples := range positives {
		for _, sql := range samples {
			if got := matching(all, sql); !slices.Equal(got, []string{name}) {
				t.Errorf("%s matches %v, want [%s]", sql, got, name)
			}
		}
	}
}

func TestForms_MatchNoPortableStatement(t *testing.T) {
	all := forms(t)
	for _, sql := range portables {
		if got := matching(all, sql); len(got) > 0 {
			t.Errorf("%s matches %v, want none", sql, got)
		}
	}
}
