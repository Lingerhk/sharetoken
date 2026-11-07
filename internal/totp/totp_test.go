package totp

import (
	"testing"
	"time"
)

// RFC 6238 appendix B vectors (SHA-1, 8 digits); we compare the last 6.
func TestRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		unix int64
		code string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"},
		{1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	} {
		if got := Code(secret, time.Unix(v.unix, 0)); got != v.code {
			t.Errorf("t=%d: got %s want %s", v.unix, got, v.code)
		}
	}
}

func TestVerifyWindowAndReplay(t *testing.T) {
	secret, err := NewSecret()
	if err != nil || len(secret) != SecretSize {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	step := Step(now)
	for _, d := range []int64{-1, 0, 1} {
		got, ok := Verify(secret, CodeAt(secret, step+d), now, 0)
		if !ok || got != step+d {
			t.Fatalf("step %+d not accepted", d)
		}
	}
	if _, ok := Verify(secret, CodeAt(secret, step+2), now, 0); ok {
		t.Fatal("code two steps ahead accepted")
	}
	if _, ok := Verify(secret, CodeAt(secret, step), now, step); ok {
		t.Fatal("replayed code accepted")
	}
	if _, ok := Verify(secret, CodeAt(secret, step-1), now, step); ok {
		t.Fatal("older code accepted after a newer one")
	}
	if _, ok := Verify(secret, "12345", now, 0); ok {
		t.Fatal("short code accepted")
	}
	if _, ok := Verify(secret, " "+CodeAt(secret, step)[:3]+" "+CodeAt(secret, step)[3:]+" ", now, 0); !ok {
		t.Fatal("spaces must be ignored")
	}
}

func TestEncodeDecodeAndURL(t *testing.T) {
	secret := []byte("12345678901234567890")
	enc := Encode(secret)
	if enc != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatalf("encode %s", enc)
	}
	dec, err := Decode("gezd gnbv gy3t qojq gezd gnbv gy3t qojq")
	if err != nil || string(dec) != string(secret) {
		t.Fatalf("decode %q %v", dec, err)
	}
	u := URL("sharetoken", "root", secret)
	want := "otpauth://totp/sharetoken:root?algorithm=SHA1&digits=6&issuer=sharetoken&period=30&secret=" + enc
	if u != want {
		t.Fatalf("url %s", u)
	}
}
