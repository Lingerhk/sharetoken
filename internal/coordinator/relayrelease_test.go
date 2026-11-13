package coordinator

import "testing"

func TestRelayRelease(t *testing.T) {
	for in, want := range map[string]string{
		"sharetoken-relay/1.0.0 (linux/amd64)":  "1.0.0",
		"sharetoken-relay/1.0.0":                "1.0.0",
		"sharetoken-client/1.0.0 (linux/amd64)": "",
		"github.com/libp2p/go-libp2p":           "",
		"":                                      "",
	} {
		if got := relayRelease(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
