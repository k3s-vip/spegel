package routing

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryRouter(t *testing.T) {
	t.Parallel()

	r := NewMemoryRouter(map[string][]Peer{}, Peer{})

	isReady, err := r.Ready(t.Context())
	require.NoError(t, err)
	require.True(t, isReady)
	r.SetReadiness(false)
	isReady, err = r.Ready(t.Context())
	require.NoError(t, err)
	require.False(t, isReady)
	r.SetReadiness(true)

	err = r.Advertise(t.Context(), []string{"foo"})
	require.NoError(t, err)
	addPeer := Peer{
		Host:      "test",
		Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		Metadata: PeerMetadata{
			RegistryPort: 9090,
		},
	}
	r.Add("foo", addPeer)
	rr, err := r.Lookup(t.Context(), "foo", 2)
	require.NoError(t, err)
	peers := []Peer{}
	for range 2 {
		peer, err := rr.Next()
		require.NoError(t, err)
		peers = append(peers, peer)
	}

	require.Len(t, peers, 2)
	peers, ok := r.Get("foo")
	require.True(t, ok)
	require.Len(t, peers, 2)

	rr, err = r.Lookup(t.Context(), "bar", 1)
	require.NoError(t, err)
	_, err = rr.Next()
	require.ErrorIs(t, err, ErrNoNext)
	_, ok = r.Get("bar")
	require.False(t, ok)
}
