package web

import (
	"strings"
	"testing"
	"time"

	"github.com/pluja/nerostr/internal/store"
	"github.com/pluja/nerostr/internal/xmr"
)

func TestMoneroURIRequestsWhatIsDue(t *testing.T) {
	amount, err := xmr.ParseXMR("0.002")
	if err != nil {
		t.Fatal(err)
	}
	seen, err := xmr.ParseXMR("0.0005")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		seen xmr.Piconero
		want string
	}{
		{"unpaid", 0, "tx_amount=0.002&"},
		{"partly paid", seen, "tx_amount=0.0015&"},
		{"fully paid", amount, "tx_amount=0.002&"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			uri := moneroURI(&store.Invoice{Address: "8Addr", Amount: amount, Seen: c.seen})
			if !strings.HasPrefix(uri, "monero:8Addr?") || !strings.Contains(uri, c.want) {
				t.Fatalf("got %q, want %q", uri, c.want)
			}
		})
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "under a minute",
		52 * time.Minute: "52 min",
		5 * time.Hour:    "5 hours",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestAddressGroups(t *testing.T) {
	groups := addressGroups("8abcd12345xyz")
	var got []string
	for _, g := range groups {
		got = append(got, g.Text)
	}
	if strings.Join(got, " ") != "8abcd 12345 xyz" {
		t.Fatalf("groups: %v", got)
	}
	if !groups[0].Edge || groups[1].Edge || !groups[2].Edge {
		t.Fatalf("only the first and last groups are edges: %+v", groups)
	}
}
