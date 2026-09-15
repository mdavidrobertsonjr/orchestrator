package hosted

import (
	"net/netip"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "64:ff9b::a00:1", "0.0.0.0", "224.0.0.1", "198.18.0.1"} {
		if publicIP(netip.MustParseAddr(value)) {
			t.Errorf("accepted %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(value)) {
			t.Errorf("rejected %s", value)
		}
	}
}
