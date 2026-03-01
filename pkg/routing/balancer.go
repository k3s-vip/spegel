package routing

import (
	"context"
	"errors"
	"net/netip"
	"sync"
)

// ErrNoNext is returned when next will result in no new peer.
var ErrNoNext = errors.New("no peers available for selection")

// Peer represents a host reachable at one or more addresses.
type Peer struct {
	Host      string
	Addresses []netip.Addr
	Metadata  PeerMetadata
}

// PeerMetadata contains additional information for the peer.
type PeerMetadata struct {
	RegistryPort uint16
}

// Balancer defines how peers looked up are returned.
type Balancer interface {
	// Next returns the next peer.
	Next() (Peer, error)
	// Size returns the amount of peers.
	Size() int
	// Add adds a peer to the balancer.
	Add(Peer)
	// Remove removes the peer from the balancer.
	Remove(Peer)
}

var _ Balancer = &RoundRobin{}

type RoundRobin struct {
	peers   []Peer
	nextIdx int
	peerMx  sync.Mutex
}

func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

func (rr *RoundRobin) Size() int {
	return len(rr.peers)
}

func (rr *RoundRobin) Add(peer Peer) {
	rr.peerMx.Lock()
	defer rr.peerMx.Unlock()

	// Skip if already exists.
	for _, p := range rr.peers {
		if p.Host == peer.Host {
			return
		}
	}

	rr.peers = append(rr.peers, peer)
}

func (rr *RoundRobin) Remove(peer Peer) {
	rr.peerMx.Lock()
	defer rr.peerMx.Unlock()

	for i, p := range rr.peers {
		if p.Host != peer.Host {
			continue
		}

		rr.peers = append(rr.peers[:i], rr.peers[i+1:]...)
		if rr.nextIdx > i {
			rr.nextIdx--
		} else if rr.nextIdx >= len(rr.peers) {
			rr.nextIdx = 0
		}
		return
	}
}

func (rr *RoundRobin) Next() (Peer, error) {
	rr.peerMx.Lock()
	defer rr.peerMx.Unlock()

	if len(rr.peers) == 0 {
		return Peer{}, ErrNoNext
	}
	peer := rr.peers[rr.nextIdx]
	rr.nextIdx = (rr.nextIdx + 1) % len(rr.peers)
	return peer, nil
}

var _ Balancer = &ClosableBalancer{}

type ClosableBalancer struct {
	Balancer
	closeCtx  context.Context
	closeFunc context.CancelFunc
	waiters   []chan any
	waitersMx sync.Mutex
}

func NewClosableBalancer(balancer Balancer) *ClosableBalancer {
	closeCtx, closeFunc := context.WithCancel(context.Background())
	return &ClosableBalancer{
		Balancer:  balancer,
		closeCtx:  closeCtx,
		closeFunc: closeFunc,
	}
}

func (cb *ClosableBalancer) Add(peer Peer) {
	cb.Balancer.Add(peer)

	cb.waitersMx.Lock()
	for _, ch := range cb.waiters {
		close(ch)
	}
	cb.waiters = nil
	cb.waitersMx.Unlock()
}

func (cb *ClosableBalancer) Next() (Peer, error) {
	for {
		cb.waitersMx.Lock()
		peer, err := cb.Balancer.Next()
		if errors.Is(err, ErrNoNext) {
			ch := make(chan any)
			cb.waiters = append(cb.waiters, ch)
			cb.waitersMx.Unlock()

			select {
			case <-cb.closeCtx.Done():
				return Peer{}, ErrNoNext
			case <-ch:
				continue
			}
		}
		cb.waitersMx.Unlock()
		if err != nil {
			return Peer{}, err
		}
		return peer, nil
	}
}

func (cb *ClosableBalancer) Close() {
	cb.closeFunc()
}
