package wg

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	Interface = "wg0"
	NFTable   = "oci_link"
	Keepalive = 25
)

type Mode string

const (
	ModeSNAT      Mode = "snat"
	ModeHostRoute Mode = "hostroute"
)

func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", ModeSNAT:
		return ModeSNAT, nil
	case ModeHostRoute:
		return ModeHostRoute, nil
	}
	return "", fmt.Errorf("WG_MODE %q is not snat or hostroute", s)
}

type Peer struct {
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
	Endpoint  string `json:"endpoint"`
	Address   string `json:"address"`
}

func ParsePeers(data []byte) ([]Peer, error) {
	var peers []Peer
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, err
	}
	for _, p := range peers {
		if err := checkPeer(p); err != nil {
			return nil, fmt.Errorf("peer %q: %w", p.Name, err)
		}
	}
	return peers, nil
}

var peerNetwork = netip.MustParsePrefix("10.200.0.0/24")

func checkPeer(p Peer) error {
	if p.Name == "" || strings.ContainsAny(p.Name, "\r\n") {
		return fmt.Errorf("name must be one non-empty line")
	}
	key, err := base64.StdEncoding.DecodeString(p.PublicKey)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != p.PublicKey {
		return fmt.Errorf("publicKey must be a base64 32-byte key")
	}
	ep, err := netip.ParseAddrPort(p.Endpoint)
	if err != nil || ep.Addr().Zone() != "" || ep.String() != p.Endpoint {
		return fmt.Errorf("endpoint must be a plain address and port")
	}
	prefix, err := netip.ParsePrefix(p.Address)
	if err != nil || prefix.Bits() != 32 || !peerNetwork.Contains(prefix.Addr()) {
		return fmt.Errorf("address must be a /32 inside %s", peerNetwork)
	}
	return nil
}

func SlotAddress(slot int) string {
	return "10.201.0." + strconv.Itoa(slot+1)
}

type Command struct {
	Name  string
	Args  []string
	Stdin string
}

func (c Command) String() string {
	return c.Name + " " + strings.Join(c.Args, " ")
}

type Desired struct {
	Slot       int
	PrivateKey string
	Peers      []Peer
	Mode       Mode
	VCN        string
}

func Config(d Desired) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\n", d.PrivateKey)
	for _, p := range d.Peers {
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %d\n", p.PublicKey, p.Endpoint, p.Address, Keepalive)
	}
	return b.String()
}

func NATRuleset(slot int) string {
	return fmt.Sprintf("table ip %[1]s\ndelete table ip %[1]s\ntable ip %[1]s {\n\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n\t\toifname \"%[2]s\" snat to %[3]s\n\t}\n}\n", NFTable, Interface, SlotAddress(slot))
}

func UpPlan(d Desired, linkExists bool) []Command {
	addr := SlotAddress(d.Slot)
	var plan []Command
	if !linkExists {
		plan = append(plan, Command{Name: "ip", Args: []string{"link", "add", "dev", Interface, "type", "wireguard"}})
	}
	plan = append(plan,
		Command{Name: "wg", Args: []string{"syncconf", Interface, "/dev/stdin"}, Stdin: Config(d)},
		Command{Name: "ip", Args: []string{"address", "replace", addr + "/32", "dev", Interface}},
		Command{Name: "ip", Args: []string{"link", "set", "up", "dev", Interface}},
	)
	if d.Mode == ModeHostRoute {
		plan = append(plan, Command{Name: "ip", Args: []string{"route", "replace", d.VCN, "dev", Interface, "src", addr}})
		return plan
	}
	for _, p := range d.Peers {
		plan = append(plan, Command{Name: "ip", Args: []string{"route", "replace", p.Address, "dev", Interface}})
	}
	plan = append(plan, Command{Name: "nft", Args: []string{"-f", "-"}, Stdin: NATRuleset(d.Slot)})
	return plan
}

func DownPlan() []Command {
	return []Command{
		{Name: "ip", Args: []string{"link", "del", "dev", Interface}},
		{Name: "nft", Args: []string{"delete", "table", "ip", NFTable}},
	}
}

type Runner interface {
	Run(c Command) error
	LinkExists() bool
	Output(name string, args ...string) ([]byte, error)
}

type Exec struct{}

func (Exec) Run(c Command) error {
	cmd := exec.Command(c.Name, c.Args...)
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", c.String(), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (Exec) LinkExists() bool {
	_, err := os.Stat("/sys/class/net/" + Interface)
	return err == nil
}

func (Exec) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type Tunnel struct {
	Runner  Runner
	Mode    Mode
	VCN     string
	Peers   func() ([]Peer, error)
	KeyFile func(slot int) string
}

func (t *Tunnel) Up(slot int) error {
	peers, err := t.Peers()
	if err != nil {
		return err
	}
	key, err := os.ReadFile(t.KeyFile(slot))
	if err != nil {
		return err
	}
	d := Desired{Slot: slot, PrivateKey: strings.TrimSpace(string(key)), Peers: peers, Mode: t.Mode, VCN: t.VCN}
	for _, c := range UpPlan(d, t.Runner.LinkExists()) {
		if err := t.Runner.Run(c); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tunnel) Down() error {
	var first error
	for _, c := range DownPlan() {
		if c.Name == "ip" && !t.Runner.LinkExists() {
			continue
		}
		if err := t.Runner.Run(c); err != nil && first == nil && !strings.Contains(err.Error(), "No such file or directory") {
			first = err
		}
	}
	return first
}
