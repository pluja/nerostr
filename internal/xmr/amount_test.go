package xmr

import "testing"

func TestParseXMR(t *testing.T) {
	cases := []struct {
		in   string
		want Piconero
	}{
		{"0.002", 2_000_000_000},
		{"1", OneXMR},
		{"1.5", OneXMR + OneXMR/2},
		{".5", OneXMR / 2},
		{"0.000000000001", 1},
		{" 0.01 ", 10_000_000_000},
		{"18446744", 18446744 * OneXMR},
	}
	for _, c := range cases {
		got, err := ParseXMR(c.in)
		if err != nil {
			t.Fatalf("ParseXMR(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseXMR(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseXMRRejects(t *testing.T) {
	for _, in := range []string{"", "-1", "1e3", "0.0000000000001", "abc", "1.2.3", "18446745", "0x10"} {
		if _, err := ParseXMR(in); err == nil {
			t.Errorf("ParseXMR(%q) should fail", in)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[Piconero]string{
		0:                   "0",
		2_000_000_000:       "0.002",
		OneXMR:              "1",
		OneXMR + 1:          "1.000000000001",
		123_456_789_000_000: "123.456789",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", in, got, want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, s := range []string{"0.002", "1", "0.000000000001", "42.1337"} {
		p, err := ParseXMR(s)
		if err != nil {
			t.Fatal(err)
		}
		if p.String() != s {
			t.Errorf("round trip %q -> %q", s, p.String())
		}
	}
}
