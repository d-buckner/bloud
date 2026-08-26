// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package mdns

import (
	"fmt"
	"net"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

const (
	mdnsPort     = 5353
	mdnsGroupV4  = "224.0.0.251"
	maxQuerySize = 65535
)

var mdnsGroupAddr = func() *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP(mdnsGroupV4), Port: mdnsPort}
}()

// Socket is the mDNS transport the Publisher sends announcements through and
// reads queries from. Production uses mdnsSocket; tests inject a fake.
type Socket interface {
	// Multicast sends msg to the mDNS group on the socket's interface.
	Multicast(msg *dns.Msg) error
	// Unicast sends msg to a single peer (required by RFC 6762 §6.4 when the
	// query sets the unicast-response bit).
	Unicast(to net.Addr, msg *dns.Msg) error
	// Read blocks until the next mDNS message arrives or the socket closes.
	Read() (*dns.Msg, net.Addr, error)
	// Close releases the socket; any blocked Read returns an error.
	Close() error
}

// mdnsSocket is a UDP socket bound to the mDNS port that has joined the mDNS
// multicast group on a single interface.
type mdnsSocket struct {
	conn *net.UDPConn
}

// newMDSNSocket binds the mDNS port and joins the multicast group on iface.
func newMDSNSocket(iface *net.Interface) (Socket, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: mdnsPort})
	if err != nil {
		return nil, fmt.Errorf("bind :%d: %w", mdnsPort, err)
	}
	pc := ipv4.NewPacketConn(conn)
	if err := pc.JoinGroup(iface, mdnsGroupAddr); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("join %s on %s: %w", mdnsGroupV4, iface.Name, err)
	}
	return &mdnsSocket{conn: conn}, nil
}

// interfaceForIP finds the interface carrying ip.
func interfaceForIP(ip net.IP) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if ok && ipNet.IP.Equal(ip) {
				return &ifaces[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no interface with address %s", ip)
}

// newSocketForIP binds the mDNS port on the interface carrying ip. It is the
// production Options.NewSocket implementation.
func newSocketForIP(ip net.IP) (Socket, error) {
	iface, err := interfaceForIP(ip)
	if err != nil {
		return nil, err
	}
	return newMDSNSocket(iface)
}

func (s *mdnsSocket) Multicast(msg *dns.Msg) error {
	buf, err := msg.Pack()
	if err != nil {
		return err
	}
	_, err = s.conn.WriteToUDP(buf, mdnsGroupAddr)
	return err
}

func (s *mdnsSocket) Unicast(to net.Addr, msg *dns.Msg) error {
	udp, ok := to.(*net.UDPAddr)
	if !ok {
		return fmt.Errorf("not a UDP address: %T", to)
	}
	buf, err := msg.Pack()
	if err != nil {
		return err
	}
	_, err = s.conn.WriteToUDP(buf, udp)
	return err
}

func (s *mdnsSocket) Read() (*dns.Msg, net.Addr, error) {
	buf := make([]byte, maxQuerySize)
	n, from, err := s.conn.ReadFromUDP(buf)
	if err != nil {
		return nil, nil, err
	}
	m := new(dns.Msg)
	if err := m.Unpack(buf[:n]); err != nil {
		return nil, nil, err
	}
	return m, from, nil
}

func (s *mdnsSocket) Close() error {
	return s.conn.Close()
}
