package canon

import (
	"bytes"
	"testing"
)

func TestLengthPrefixPreventsAmbiguity(t *testing.T) {
	a := New("d").String("ab").String("c").Sum()
	b := New("d").String("a").String("bc").Sum()
	if bytes.Equal(a, b) {
		t.Fatal(`("ab","c") and ("a","bc") must encode differently`)
	}
}

func TestDomainSeparation(t *testing.T) {
	a := New("sharetoken/ticket/v1").U64(1).Sum()
	b := New("sharetoken/offer/v1").U64(1).Sum()
	if bytes.Equal(a, b) {
		t.Fatal("different domains must produce different encodings")
	}
}

func TestDeterministic(t *testing.T) {
	enc := func() []byte {
		return New("d").Bytes([]byte{1, 2}).Strings([]string{"x", "y"}).I64(-5).Bool(true).Sum()
	}
	if !bytes.Equal(enc(), enc()) {
		t.Fatal("encoding is not deterministic")
	}
}

func TestNilAndEmptyEncodeTheSame(t *testing.T) {
	// JSON round trips turn empty slices into nil and back; signatures must
	// survive that.
	if !bytes.Equal(New("d").Bytes(nil).Strings(nil).Sum(), New("d").Bytes([]byte{}).Strings([]string{}).Sum()) {
		t.Fatal("nil and empty must encode identically")
	}
}
