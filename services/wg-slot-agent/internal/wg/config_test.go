package wg

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var peers = []Peer{
	{Name: "a1-1", PublicKey: "pubA", Endpoint: "203.0.113.11:51820", Address: "10.200.0.11/32"},
	{Name: "a1-2", PublicKey: "pubB", Endpoint: "203.0.113.12:51820", Address: "10.200.0.12/32"},
	{Name: "micro-1", PublicKey: "pubC", Endpoint: "203.0.113.13:51820", Address: "10.200.0.13/32"},
}

func lines(plan []Command) []string {
	var out []string
	for _, c := range plan {
		out = append(out, c.String())
	}
	return out
}

func TestUpPlanSNAT(t *testing.T) {
	plan := UpPlan(Desired{Slot: 2, PrivateKey: "priv", Peers: peers, Mode: ModeSNAT}, false)
	want := []string{
		"ip link add dev wg0 type wireguard",
		"wg syncconf wg0 /dev/stdin",
		"ip address replace 10.201.0.3/32 dev wg0",
		"ip link set up dev wg0",
		"ip route replace 10.200.0.11/32 dev wg0",
		"ip route replace 10.200.0.12/32 dev wg0",
		"ip route replace 10.200.0.13/32 dev wg0",
		"nft -f -",
	}
	if got := lines(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	conf := plan[1].Stdin
	for _, p := range peers {
		block := "PublicKey = " + p.PublicKey + "\nEndpoint = " + p.Endpoint + "\nAllowedIPs = " + p.Address + "\nPersistentKeepalive = 25\n"
		if !strings.Contains(conf, block) {
			t.Fatalf("config lacks peer block for %s:\n%s", p.Name, conf)
		}
	}
	if strings.Count(conf, "[Peer]") != 3 {
		t.Fatalf("config has %d peers, want 3", strings.Count(conf, "[Peer]"))
	}
	nft := plan[len(plan)-1].Stdin
	if strings.Count(nft, "snat to") != 1 || !strings.Contains(nft, `oifname "wg0" snat to 10.201.0.3`) {
		t.Fatalf("nft ruleset must hold exactly one rule `oifname \"wg0\" snat to 10.201.0.3`:\n%s", nft)
	}
	if !strings.Contains(nft, "table ip oci_link {") {
		t.Fatalf("nft ruleset must define table oci_link:\n%s", nft)
	}
}

func TestUpPlanIsIdempotent(t *testing.T) {
	d := Desired{Slot: 0, PrivateKey: "priv", Peers: peers, Mode: ModeSNAT}
	again := UpPlan(d, true)
	for _, c := range again {
		if c.String() == "ip link add dev wg0 type wireguard" {
			t.Fatal("re-run adds wg0 again")
		}
		if c.Name == "ip" && len(c.Args) > 1 && c.Args[1] == "add" {
			t.Fatalf("re-run uses a non-idempotent add: %s", c)
		}
	}
	nft := again[len(again)-1].Stdin
	if !strings.HasPrefix(nft, "table ip oci_link\ndelete table ip oci_link\n") {
		t.Fatalf("nft ruleset must replace the table atomically:\n%s", nft)
	}
}

func TestUpPlanHostRoute(t *testing.T) {
	plan := UpPlan(Desired{Slot: 1, PrivateKey: "priv", Peers: peers, Mode: ModeHostRoute, VCN: "10.200.0.0/24"}, true)
	got := lines(plan)
	want := "ip route replace 10.200.0.0/24 dev wg0 src 10.201.0.2"
	if got[len(got)-1] != want {
		t.Fatalf("last command = %q, want %q", got[len(got)-1], want)
	}
	for _, c := range plan {
		if c.Name == "nft" {
			t.Fatal("hostroute mode must not create an nft table")
		}
	}
}

func TestDownPlanTouchesOnlyOwnObjects(t *testing.T) {
	want := []string{"ip link del dev wg0", "nft delete table ip oci_link"}
	if got := lines(DownPlan()); !reflect.DeepEqual(got, want) {
		t.Fatalf("down plan = %v, want %v", got, want)
	}
}

type recorder struct {
	link bool
	ran  []Command
	fail map[string]error
}

func (r *recorder) Run(c Command) error {
	r.ran = append(r.ran, c)
	if c.String() == "ip link add dev wg0 type wireguard" {
		r.link = true
	}
	if c.String() == "ip link del dev wg0" {
		r.link = false
	}
	return r.fail[c.String()]
}
func (r *recorder) LinkExists() bool                         { return r.link }
func (r *recorder) Output(string, ...string) ([]byte, error) { return nil, nil }

func TestTunnelUpTwiceThenDown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slot-0.key"), []byte("priv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{fail: map[string]error{"nft delete table ip oci_link": errors.New("Error: Could not process rule: No such file or directory")}}
	tun := &Tunnel{Runner: r, Mode: ModeSNAT, Peers: func() ([]Peer, error) { return peers, nil }, KeyFile: func(n int) string { return filepath.Join(dir, "slot-0.key") }}
	if err := tun.Up(0); err != nil {
		t.Fatal(err)
	}
	first := len(r.ran)
	if err := tun.Up(0); err != nil {
		t.Fatal(err)
	}
	if len(r.ran)-first != first-1 {
		t.Fatalf("second Up ran %d commands, want %d (no link add)", len(r.ran)-first, first-1)
	}
	if !strings.Contains(r.ran[1].Stdin, "PrivateKey = priv\n") {
		t.Fatalf("key not trimmed into config: %q", r.ran[1].Stdin)
	}
	if err := tun.Down(); err != nil {
		t.Fatalf("Down with an absent table: %v", err)
	}
}

func TestParsePeersRejectsWideAddress(t *testing.T) {
	_, err := ParsePeers([]byte(`[{"name":"a1-1","publicKey":"k","endpoint":"203.0.113.11:51820","address":"10.200.0.0/24"}]`))
	if err == nil {
		t.Fatal("a /24 peer address was accepted")
	}
}

func TestParsePeersRejectsUnsafeValues(t *testing.T) {
	good := `{"name":"broker-1","publicKey":"` + strings.Repeat("A", 43) + `=","endpoint":"203.0.113.10:51820","address":"10.200.0.11/32"}`
	if _, err := ParsePeers([]byte("[" + good + "]")); err != nil {
		t.Fatalf("good peer rejected: %v", err)
	}
	bad := map[string]string{
		"newline in key":    strings.Replace(good, `=","endpoint`, `=\n","endpoint`, 1),
		"zone in endpoint":  strings.Replace(good, "203.0.113.10:51820", `[fe80::1%a\n[Peer]\nPublicKey = x]:51820`, 1),
		"padded endpoint":   strings.Replace(good, "203.0.113.10:51820", "203.0.113.010:51820", 1),
		"short key":         strings.Replace(good, strings.Repeat("A", 43)+"=", "AAAA", 1),
		"hostname endpoint": strings.Replace(good, "203.0.113.10:51820", "evil.example:51820", 1),
		"no port":           strings.Replace(good, "203.0.113.10:51820", "203.0.113.10", 1),
		"wide address":      strings.Replace(good, "10.200.0.11/32", "0.0.0.0/0", 1),
		"outside vcn":       strings.Replace(good, "10.200.0.11/32", "10.96.0.1/32", 1),
		"newline in name":   strings.Replace(good, `"broker-1"`, `"broker-1\nPostUp"`, 1),
	}
	for name, peer := range bad {
		if _, err := ParsePeers([]byte("[" + peer + "]")); err == nil {
			t.Errorf("%s: accepted %s", name, peer)
		}
	}
}

func TestCommittedWGPeersJSONParsesCleanly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "clusters", "gke", "oci-link", "wg-peers.json"))
	if err != nil {
		t.Fatalf("read committed wg-peers.json: %v", err)
	}
	got, err := ParsePeers(data)
	if err != nil {
		t.Fatalf("ParsePeers(committed wg-peers.json): %v", err)
	}
	wantAddrs := map[string]string{
		"broker-1": "10.200.0.11/32",
		"broker-2": "10.200.0.12/32",
		"broker-3": "10.200.0.13/32",
	}
	if len(got) != len(wantAddrs) {
		t.Fatalf("got %d peers, want %d", len(got), len(wantAddrs))
	}
	seen := make(map[string]string, len(got))
	for _, p := range got {
		seen[p.Name] = p.Address
	}
	if !reflect.DeepEqual(seen, wantAddrs) {
		t.Fatalf("peers = %v, want %v", seen, wantAddrs)
	}
}
