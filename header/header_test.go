package header_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/sqlate/header"
)

func parse(t *testing.T, text string) header.Header {
	t.Helper()
	h, err := header.Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return h
}

func TestParse_ReadsDirectivesUntilTheFirstSQLToken(t *testing.T) {
	text := `
--| tier: native
--|   native: postgres — pg_advisory_xact_lock. Ports: sp_getapplock, GET_LOCK.
-- a line of prose, not a declaration
--| field: id uuid

--| field: name text
SELECT pg_advisory_xact_lock({{key}})
-- transaction: none  (a plain comment in the body)
`
	h := parse(t, text)
	if v, ok := h.Get("tier"); !ok || v != "native" {
		t.Errorf("tier = %q, %v", v, ok)
	}
	if v, _ := h.Get("native"); v != "postgres — pg_advisory_xact_lock. Ports: sp_getapplock, GET_LOCK." {
		t.Errorf("native = %q", v)
	}
	if got := h.All("field"); !slices.Equal(got, []string{"id uuid", "name text"}) {
		t.Errorf("fields = %v", got)
	}
	if _, ok := h.Get("transaction"); ok {
		t.Error("a plain comment was read as a declaration")
	}
	if got := h.Keys(); !slices.Equal(got, []string{"tier", "native", "field"}) {
		t.Errorf("keys = %v", got)
	}
	if ds := h.Declarations(); ds[0].Line != 2 || ds[3].Line != 7 {
		t.Errorf("line numbers = %d, %d", ds[0].Line, ds[3].Line)
	}
	if body := text[h.End():]; !strings.HasPrefix(body, "SELECT pg_advisory_xact_lock") {
		t.Errorf("body = %q", body)
	}
}

func TestParse_EndIsWhereTheBodyBegins(t *testing.T) {
	if h := parse(t, "--| tier: standard\n"); h.End() != len("--| tier: standard\n") {
		t.Errorf("header-only End = %d", h.End())
	}
	if h := parse(t, "SELECT 1"); h.End() != 0 {
		t.Errorf("headerless End = %d", h.End())
	}
	if h := parse(t, "-- prose only\nSELECT 1"); h.End() != len("-- prose only\n") || len(h.Declarations()) != 0 {
		t.Errorf("prose-only header: End = %d, declarations = %v", h.End(), h.Declarations())
	}
}

func TestParse_PlainCommentsAreProse(t *testing.T) {
	for _, text := range []string{"", "SELECT 1", "-- just a comment\nSELECT 1", "-- tier: standard\nSELECT 1"} {
		if h := parse(t, text); len(h.Declarations()) != 0 {
			t.Errorf("%q: declarations = %v", text, h.Declarations())
		}
	}
	if v, ok := parse(t, "--| key:\nSELECT 1").Get("key"); !ok || v != "" {
		t.Errorf("empty value: %q, %v", v, ok)
	}
}

func TestParse_RejectsMalformedAndMisplacedDirectives(t *testing.T) {
	cases := map[string]string{
		"no key":                 "--| just words\nSELECT 1",
		"uppercase key":          "--| Tier: standard\nSELECT 1",
		"declaration after body": "--| tier: standard\nSELECT 1\n--| key: id",
	}
	for name, text := range cases {
		if _, err := header.Parse(text); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
