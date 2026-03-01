package routing

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClosableBalancer(t *testing.T) {
	t.Parallel()

	peer := Peer{
		Host:      "test",
		Addresses: []netip.Addr{netip.MustParseAddr("192.168.1.25")},
		Metadata: PeerMetadata{
			RegistryPort: 9797,
		},
	}

	rr := NewRoundRobin()
	require.Equal(t, 0, rr.Size())
	_, err := rr.Next()
	require.ErrorIs(t, ErrNoNext, err)
	rr.Remove(peer)

	for range 3 {
		rr.Add(peer)
	}
	require.Equal(t, 1, rr.Size())
	next, err := rr.Next()
	require.NoError(t, err)
	require.Equal(t, peer, next)

	rr.Remove(peer)
	require.Equal(t, 0, rr.Size())
	_, err = rr.Next()
	require.ErrorIs(t, ErrNoNext, err)

	// Test that we can call close multiple times.
	cb := NewClosableBalancer(rr)
	for range 3 {
		cb.Close()
	}
}
