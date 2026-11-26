package connect

import (
	"bytes"
	"encoding/json"
	"errors"
)

// object is a JSON object that keeps its keys' order: the user's file comes
// back as it was, but for the keys changed.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObject() *object { return &object{vals: map[string]json.RawMessage{}} }

func parseObject(data []byte) (*object, error) {
	o := newObject()
	if len(bytes.TrimSpace(data)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.set(k, v)
	}
	return o, nil
}

func (o *object) get(k string) (json.RawMessage, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) setValue(k string, v any) {
	b, _ := json.Marshal(v)
	o.set(k, b)
}

func (o *object) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) child(k string) *object {
	if v, ok := o.vals[k]; ok {
		if c, err := parseObject(v); err == nil {
			return c
		}
	}
	return newObject()
}

func (o *object) compact() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (o *object) indented() []byte {
	var b bytes.Buffer
	if err := json.Indent(&b, o.compact(), "", "  "); err != nil {
		return o.compact()
	}
	b.WriteByte('\n')
	return b.Bytes()
}
