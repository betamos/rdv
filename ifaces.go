package rdv

import (
	"errors"
	"net"
	"net/netip"
)

type Interfaces []Interface

type Interface struct {
	net.Interface
	Addrs []netip.Prefix
}

func Query() (Interfaces, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var ifaces Interfaces
	for _, nif := range ifs {
		naddrs, err := nif.Addrs()
		if err != nil {
			return nil, err
		}
		var prefixes []netip.Prefix
		for _, naddr := range naddrs {
			ipnet := naddr.(*net.IPNet)
			prefix := ipnet2Prefix(ipnet)
			prefixes = append(prefixes, prefix)
		}
		ifaces = append(ifaces, Interface{Interface: nif, Addrs: prefixes})
	}
	return ifaces, nil
}

// convert net.IPNet to netip.Prefix
func ipnet2Prefix(ipn *net.IPNet) netip.Prefix {
	addr, _ := netip.AddrFromSlice(ipn.IP)
	cidr, _ := ipn.Mask.Size()
	return netip.PrefixFrom(addr.Unmap(), cidr)
}

// Returns index of interface for an addr, or -1 if not found.
func (ifs Interfaces) FindByAddr(ip netip.Addr) (int, *Interface) {
	for idx, iface := range ifs {
		if iface.Contains(ip) {
			return idx, &iface
		}
	}
	return -1, nil
}

func (i Interface) Contains(ip netip.Addr) bool {
	for _, prefix := range i.Addrs {
		if prefix.Addr() == ip {
			return true
		}
	}
	return false
}

func (i Interface) SpaceMap() map[AddrSpace][]netip.Prefix {
	m := make(map[AddrSpace][]netip.Prefix)
	for _, prefix := range i.Addrs {
		space := GetAddrSpace(prefix.Addr())
		m[space] = append(m[space], prefix)
	}
	return m
}

func (i Interface) FirstAddrs() (addrs []netip.Prefix) {
	iaddrs := i.SpaceMap()
	for _, addrSlice := range iaddrs {
		addrs = append(addrs, addrSlice[0])
	}
	return
}

func (i Interface) MatchSpace(spaces AddrSpace) (filtered []netip.Prefix) {
	for _, prefix := range i.Addrs {
		space := GetAddrSpace(prefix.Addr())
		if spaces.Includes(space) {
			filtered = append(filtered, prefix)
		}
	}
	return
}

func DefaultInterface() (iface *Interface, err error) {
	laddr, _ := localNetIpFor(v4pub)

	ifaces, _ := Query()
	var idx int
	idx, iface = ifaces.FindByAddr(laddr)
	if idx == -1 {
		return nil, errors.New("no default iface found")
	}
	return iface, nil
}
