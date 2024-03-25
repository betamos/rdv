package rdv

import (
	"log"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestGetAddrSpace(t *testing.T) {
	tests := map[string]struct {
		addr  string
		space AddrSpace
	}{
		"loopback4":  {addr: "127.0.0.1", space: SpaceLoopback},
		"loopback6":  {addr: "::1", space: SpaceLoopback},
		"private4":   {addr: "192.168.0.2", space: SpacePrivate4},
		"private6":   {addr: "fd00::1", space: SpacePrivate6},
		"private6-2": {addr: "fd12:3456:789a:1::1", space: SpacePrivate6},
		"link6":      {addr: "fe80::1234", space: SpaceLink6},
		"link6_zone": {addr: "fe80::1234%%en0", space: SpaceLink6},
		"link4":      {addr: "169.254.12.1", space: SpaceLink4},
		"public4":    {addr: "213.213.213.213", space: SpacePublic4},
		"public6":    {addr: "2003::1", space: SpacePublic6},
		"tailscale":  {addr: "100.86.144.76", space: SpacePublic4},
		"zero4":      {addr: "0.0.0.0", space: SpaceInvalid},
		"zero6":      {addr: "::", space: SpaceInvalid},
		"broadcast":  {addr: "255.255.255.255", space: SpaceInvalid},
		"multicast4": {addr: "224.0.0.251", space: SpaceInvalid},
		"multicast6": {addr: "ff02::fb", space: SpaceInvalid},
		"v4mapped":   {addr: "::ffff:192.0.2.128", space: SpacePublic6}, // TODO: What to do?
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tc.addr)
			if err != nil {
				t.Fatal(err)
			}
			space := GetAddrSpace(addr)
			if space != tc.space {
				t.Fatalf("expected %v, got %v", tc.space, space)
			}
		})
	}
}

func TestLinkLocalRoute(t *testing.T) {

	laddr, _ := netip.ParseAddr(`fd00::2`)
	//laddr = netip.IPv6Unspecified()
	laddrPort := netip.AddrPortFrom(laddr, 42004)
	d := &net.Dialer{
		LocalAddr: net.TCPAddrFromAddrPort(laddrPort),
		Timeout:   3 * time.Second,
	}
	//c, err := d.Dial("tcp", v6pub)
	c, err := d.Dial("tcp", "google.com:80")
	if err != nil {
		t.Fatalf("nope: %v", err)
	}
	t.Log(c.LocalAddr(), c.RemoteAddr())
	t.Fail()
}

func TestDefaultRoute(t *testing.T) {

	laddr, err := localIpFor(v6pub)
	t.Log(laddr, err)
	t.Fail()
}

func TestIfaces(t *testing.T) {

	ifs, _ := net.Interfaces()
	t.Fatal(ifs)

}

func TestQuery(t *testing.T) {

	laddr, _ := localNetIpFor(v4pub)

	ifaces, _ := Query()
	_, iface := ifaces.FindByAddr(laddr)
	spaceMap := iface.SpaceMap()
	t.Log(spaceMap)
	var toUse []netip.Addr
	for space, addrs := range spaceMap {
		if DefaultSpaces.Includes(space) {
			toUse = append(toUse, addrs[0].Addr())
		}
	}
	t.Log(toUse)
	//for _, iface := range ifaces {
	//addrs := iface.MatchSpace(DefaultSpaces)
	// if len(addrs) == 0 {
	// 	continue
	// }
	// for _, addr := range addrs {
	// 	routable := localIsPubliclyRoutable(addr.Addr())
	// 	t.Log(iface.Index, iface.Name, addr, routable)
	// }
	//}
	t.Fail()
}

func TestAddrSpaceIncluded(t *testing.T) {
	var spaces AddrSpace = SpacePrivate4 | SpacePublic6
	if !spaces.Includes(SpacePrivate4) {
		log.Fatalln("expected private4 included")
	}
	if !spaces.Includes(SpacePublic6) {
		log.Fatalln("expected public6 included")
	}
	if spaces.Includes(SpaceLoopback) {
		log.Fatalln("expected loopback to not be included")
	}
	if spaces.Includes(SpaceInvalid) {
		log.Fatalln("expected invalid to not be included")
	}

	if !AllSpaces.Includes(SpacePrivate4) {
		log.Fatalln("all: expected private4 be included")
	}
	if AllSpaces.Includes(SpaceInvalid) {
		log.Fatalln("all: expected invalid to not be included")
	}
	if NoSpaces.Includes(SpacePrivate4) {
		log.Fatalln("no: expected private4 to not be included")
	}
	if NoSpaces.Includes(SpaceInvalid) {
		log.Fatalln("no: expected invalid to not be included")
	}
}
