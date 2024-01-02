package rdv

import (
	"log"
	"net/netip"
	"testing"
)

func TestGetAddrSpace(t *testing.T) {
	tests := map[string]struct {
		addr  string
		space AddrSpace
	}{
		"loopback4":  {addr: "127.0.0.1", space: SpaceLoopback},
		"loopback6":  {addr: "::1", space: SpaceLoopback},
		"private4":   {addr: "192.168.0.2", space: SpacePrivate4},
		"private6":   {addr: "fd12:3456:789a:1::1", space: SpacePrivate6},
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

func TestIncluded(t *testing.T) {
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
}
