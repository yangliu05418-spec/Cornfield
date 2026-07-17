package safehttp

import (
	"net/netip"
	"testing"
)

func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{address: "1.1.1.1", public: true},
		{address: "192.31.196.1", public: true},
		{address: "2606:4700:4700::1111", public: true},
		{address: "0.1.2.3", public: false},
		{address: "127.0.0.1", public: false},
		{address: "10.0.0.1", public: false},
		{address: "100.64.0.1", public: false},
		{address: "169.254.169.254", public: false},
		{address: "172.31.255.255", public: false},
		{address: "192.0.0.8", public: false},
		{address: "192.0.2.1", public: false},
		{address: "192.88.99.2", public: false},
		{address: "192.168.1.1", public: false},
		{address: "198.18.0.1", public: false},
		{address: "198.51.100.1", public: false},
		{address: "203.0.113.1", public: false},
		{address: "240.0.0.1", public: false},
		{address: "::1", public: false},
		{address: "::2", public: false},
		{address: "::ffff:127.0.0.1", public: false},
		{address: "64:ff9b::7f00:1", public: false},
		{address: "64:ff9b:1::1", public: false},
		{address: "100::1", public: false},
		{address: "100:0:0:1::1", public: false},
		{address: "2001:2::1", public: false},
		{address: "2001:db8::1", public: false},
		{address: "2002::1", public: false},
		{address: "3fff::1", public: false},
		{address: "5f00::1", public: false},
		{address: "fc00::1", public: false},
		{address: "fe80::1", public: false},
		{address: "ff02::1", public: false},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			address := netip.MustParseAddr(test.address)
			if got := isPublicIP(address); got != test.public {
				t.Fatalf("expected public=%t, got %t", test.public, got)
			}
		})
	}
}
