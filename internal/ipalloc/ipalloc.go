// Package ipalloc assigns deterministic IPv4 addresses within the Umbra subnet.
package ipalloc

import (
	"fmt"
	"net"
)

func Allocate(subnet, gateway string, firstHost int, used []string) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", err
	}
	usedSet := map[string]bool{gateway: true}
	for _, u := range used {
		usedSet[u] = true
	}
	base := ipnet.IP.To4()
	if base == nil {
		return "", fmt.Errorf("subnet %s is not IPv4", subnet)
	}
	ones, bits := ipnet.Mask.Size()
	max := 1 << (bits - ones)
	for host := firstHost; host < max-1; host++ { // -1 skips broadcast
		ip := make(net.IP, 4)
		copy(ip, base)
		v := uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
		v += uint32(host)
		cand := net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
		if !usedSet[cand] {
			return cand, nil
		}
	}
	return "", fmt.Errorf("subnet %s exhausted", subnet)
}

func Validate(subnet, gateway, ip string) error {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return err
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("%q is not an IPv4 address", ip)
	}
	if !ipnet.Contains(parsed) {
		return fmt.Errorf("%s is outside subnet %s", ip, subnet)
	}
	if ip == gateway {
		return fmt.Errorf("%s is the gateway", ip)
	}
	network := ipnet.IP.String()
	if ip == network {
		return fmt.Errorf("%s is the network address", ip)
	}
	return nil
}
