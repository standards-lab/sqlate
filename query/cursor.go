package query

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
)

// Cursor is an opaque position in a collection read's ordering: the keyed
// values of the last item of a page and a fingerprint of the filters that
// produced it, issued as Collection.Next and accepted back by Continue under
// the same filters. It is URL-safe text a consumer relays verbatim; its
// content is not a contract and a consumer never builds one.
type Cursor string

const cursorVersion = 1

type cursorBody struct {
	V       int      `json:"v"`
	Base    string   `json:"base"`
	Terms   []string `json:"terms"`
	Desc    bool     `json:"desc"`
	Filters string   `json:"filters"`
	Values  []string `json:"values"`
}

func filterSignature(filters []Filter) (string, bool) {
	if len(filters) == 0 {
		return "", true
	}
	b, err := json.Marshal(filters)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (p Projection[T]) signature(o ordering) string {
	var sb strings.Builder
	sb.WriteString(p.base.name)
	for _, t := range o.keyed {
		sb.WriteByte(0)
		sb.WriteString(t.field.Name)
		sb.WriteByte(' ')
		sb.WriteString(t.field.Type)
	}
	sb.WriteByte(0)
	if o.desc {
		sb.WriteString("desc")
	} else {
		sb.WriteString("asc")
	}
	return sb.String()
}

func check(signature string, body []byte) [4]byte {
	h := sha256.New()
	h.Write([]byte(signature))
	h.Write([]byte{0})
	h.Write(body)
	var out [4]byte
	copy(out[:], h.Sum(nil))
	return out
}

func keyedNames(o ordering) []string {
	names := make([]string, len(o.keyed))
	for i, t := range o.keyed {
		names[i] = t.field.Name
	}
	return names
}

func (p Projection[T]) encodeCursor(o ordering, filterSig string, values []string) Cursor {
	body, err := json.Marshal(cursorBody{V: cursorVersion, Base: p.base.name, Terms: keyedNames(o), Desc: o.desc, Filters: filterSig, Values: values})
	if err != nil {
		panic(err)
	}
	c := check(p.signature(o), body)
	raw := append(c[:], body...)
	return Cursor(base64.RawURLEncoding.EncodeToString(raw))
}

func (p Projection[T]) decodeCursor(c Cursor, o ordering, filters []Filter) ([]string, error) {
	if !o.cursorable {
		return nil, &CursorError{Reason: CursorUnsupported}
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil || len(raw) < 4 {
		return nil, &CursorError{Reason: CursorMalformed}
	}
	var body cursorBody
	if err := json.Unmarshal(raw[4:], &body); err != nil || body.V != cursorVersion || len(body.Values) != len(body.Terms) {
		return nil, &CursorError{Reason: CursorMalformed}
	}
	filterSig, ok := filterSignature(filters)
	if !ok || body.Base != p.base.name || !slices.Equal(body.Terms, keyedNames(o)) || body.Desc != o.desc || body.Filters != filterSig {
		return nil, &CursorError{Reason: CursorMismatch}
	}
	if check(p.signature(o), raw[4:]) != [4]byte(raw[:4]) {
		return nil, &CursorError{Reason: CursorMalformed}
	}
	return body.Values, nil
}
