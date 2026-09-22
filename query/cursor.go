package query

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
)

// Cursor is an opaque position in a collection read's ordering: the keyed
// values of the last item of a page, issued as Collection.Next and accepted
// back by Continue. It is URL-safe text a consumer relays verbatim; its
// content is not a contract and a consumer never builds one.
type Cursor string

// cursorVersion is the body format; a cursor of another version is malformed.
const cursorVersion = 1

// cursorBody is a cursor's decoded content. It names the base and the keyed
// terms so a cursor from before a contract change is caught by name, and
// the terms' declared types are covered by the check prefix instead.
type cursorBody struct {
	V      int      `json:"v"`
	Base   string   `json:"base"`
	Terms  []string `json:"terms"`
	Desc   bool     `json:"desc"`
	Values []string `json:"values"`
}

// signature is what a cursor is bound to beyond its body: the base's name
// and each keyed term's name, declared type, and direction. It is hashed
// with the body, so a cursor issued when a keyed field had another type no
// longer verifies. The hash is unkeyed and guards against accident, not
// forgery: a cursor's values re-enter the read only as bound parameters cast
// to the fields' types, so a forged one can at most select another page.
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

// check is the 4-byte prefix over a signature and a body's exact bytes.
func check(signature string, body []byte) [4]byte {
	h := sha256.New()
	h.Write([]byte(signature))
	h.Write([]byte{0})
	h.Write(body)
	var out [4]byte
	copy(out[:], h.Sum(nil))
	return out
}

// keyedNames is the keyed terms' field names, what a cursor records and a
// later request is checked against.
func keyedNames(o ordering) []string {
	names := make([]string, len(o.keyed))
	for i, t := range o.keyed {
		names[i] = t.field.Name
	}
	return names
}

// encodeCursor issues the cursor that continues past values under o: the
// body and its check prefix as one base64 string.
func (p Projection[T]) encodeCursor(o ordering, values []string) Cursor {
	body, err := json.Marshal(cursorBody{V: cursorVersion, Base: p.base.name, Terms: keyedNames(o), Desc: o.desc, Values: values})
	if err != nil {
		// The body holds strings, bools, and an int only, which always marshal.
		panic(err)
	}
	c := check(p.signature(o), body)
	raw := append(c[:], body...)
	return Cursor(base64.RawURLEncoding.EncodeToString(raw))
}

// decodeCursor reads the keyed values back out of c, refusing a request o
// cannot continue by cursor, a cursor that does not decode or verify, and
// one issued for another base or ordering.
func (p Projection[T]) decodeCursor(c Cursor, o ordering) ([]string, error) {
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
	if body.Base != p.base.name || !slices.Equal(body.Terms, keyedNames(o)) || body.Desc != o.desc {
		return nil, &CursorError{Reason: CursorMismatch}
	}
	if check(p.signature(o), raw[4:]) != [4]byte(raw[:4]) {
		return nil, &CursorError{Reason: CursorMalformed}
	}
	return body.Values, nil
}
