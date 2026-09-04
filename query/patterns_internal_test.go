package query

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestPatterns_EveryPatternDeclaresItsTierAndSlots(t *testing.T) {
	c := MustCatalog(Patterns())
	if c.builtin != Namespace {
		t.Errorf("builtin namespace = %q", c.builtin)
	}
	p, ok := c.lookup(Namespace, "collection")
	if !ok || len(p.slots) != 4 || p.slots[0] != "base" {
		t.Errorf("collection slots = %v", p.slots)
	}
	if n := len(c.namespaces[Namespace]); n != 22 {
		t.Errorf("builtin holds %d patterns", n)
	}
}

func TestRender_PanicsOnAnUnfilledSlot(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("render did not panic")
		}
	}()
	MustCatalog(Patterns()).render("collection", map[string]string{"base": "SELECT 1"})
}

func TestRender_PanicsWithoutTheLibrary(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("render did not panic")
		}
	}()
	MustCatalog(Publish("app", fstest.MapFS{"p/x.sql": {Data: []byte("--| tier: standard\nSELECT 1")}}, "p")).render("count", nil)
}

func TestReadLayer_RejectsMalformedPatterns(t *testing.T) {
	cases := map[string]string{
		"no tier":                 "SELECT {{a}}",
		"tier \"loose\"":          "--| tier: loose\nSELECT 1",
		"native pattern declares": "--| tier: native\nSELECT 1",
		"no native declaration":   "--| tier: standard\n--| native: postgres\nSELECT 1",
		"includes no pattern":     "--| tier: standard\n{{> sql.where}}",
	}
	for want, text := range cases {
		_, err := readLayer("t", layer{fstest.MapFS{"f/x.sql": {Data: []byte(text)}}, "f"})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "x.sql (t)") {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}
