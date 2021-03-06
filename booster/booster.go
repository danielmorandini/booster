/*
Copyright (C) 2018 Daniel Morandini

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

// Package booster provides the higher interface for dealing with booster instances
// that follow the booster protocol. It wraps together node, proxy, network.
package booster

import (
	"context"
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/danielmorandini/booster/log"
	"github.com/danielmorandini/booster/network"
	"github.com/danielmorandini/booster/network/packet"
	"github.com/danielmorandini/booster/node"
	"github.com/danielmorandini/booster/protocol"
	"github.com/danielmorandini/booster/pubsub"
	"github.com/danielmorandini/booster/socks5"
)

// Proxy wraps that booster requires a proxy to implement.
type Proxy interface {
	Sub(cmd *pubsub.Command) (pubsub.CancelFunc, error)

	// ListenAndServe should starts the actual proxy server, announcing it to the local
	// address.
	ListenAndServe(ctx context.Context, port int) error

	// Proto returns the string representation of the protocol used by the proxy.
	// Example: socks5.
	Proto() string
}

// PubSub describes the required functionalities of a publication/subscription object.
type PubSub interface {
	Sub(cmd *pubsub.Command) (pubsub.CancelFunc, error)
	Pub(message interface{}, topic string)
}

type SendConsumeCloser interface {
	SendCloser
	Consume() (<-chan *packet.Packet, error)
}

type SendCloser interface {
	Close() error
	Send(p *packet.Packet) error
}

// Booster wraps the parts that compose a booster node together.
type Booster struct {
	ID string

	Proxy Proxy
	PubSub

	Netconfig network.Config
	stop      chan struct{}
	restart   chan struct{}
}

var DefaultNetConfig = network.Config{
	TagSet: packet.TagSet{
		PacketOpeningTag: protocol.PacketOpeningTag,
		PacketClosingTag: protocol.PacketClosingTag,
		ModuleOpeningTag: protocol.ModuleOpeningTag,
		ModuleClosingTag: protocol.ModuleClosingTag,
		Separator:        protocol.Separator,
	},
}

// New creates a new configured booster node. Creates a network configuration
// based in the information contained in the protocol package.
//
// The internal proxy is configured to use the node dispatcher as network
// dialer.
func New(pport, bport int) (*Booster, error) {
	b := new(Booster)

	pp := strconv.Itoa(pport)
	bp := strconv.Itoa(bport)
	rn, err := node.New("localhost", pp, bp, true)
	if err != nil {
		return nil, err
	}

	id := sha1Hash([]byte(strconv.Itoa(pport)), []byte(strconv.Itoa(bport)))
	n := NewNet(rn, id)
	pubsub := pubsub.New()
	dialer := node.NewDispatcher(n)
	proxy := socks5.New(dialer)
	Nets.Set(id, n)

	b.ID = id
	b.Proxy = proxy
	b.PubSub = pubsub
	b.Netconfig = DefaultNetConfig
	b.stop = make(chan struct{})
	b.restart = make(chan struct{})

	b.Net().HeartbeatTTL = time.Second * 8
	b.Net().DialTimeout = time.Second * 4

	return b, nil
}

func (b *Booster) Net() *Network {
	return Nets.Get(b.ID)
}

// Run starts the proxy and booster node.
//
// This is a blocking routine that can be stopped using the Close() method.
// Traps INTERRUPT signals.
func (b *Booster) Run() error {
	// trap exit signals
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)

		for sig := range c {
			log.Info.Printf("booster: signal (%v) received: exiting...", sig)
			b.Close()
			return
		}
	}()

	errc := make(chan error)
	var ctx context.Context
	var cancel context.CancelFunc
	run := func() {
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()

		errc <- b.run(ctx)
	}

	for {
		go run()

		select {
		case err := <-errc:
			cancel()
			return err
		case <-b.stop:
			cancel()
			<-errc
			return fmt.Errorf("booster: stopped")
		case <-b.restart:
			cancel()
			<-errc
		}
	}

}

func (b *Booster) run(ctx context.Context) error {
	_, pport, _ := net.SplitHostPort(Nets.Get(b.ID).LocalNode.PAddr.String())
	_, bport, _ := net.SplitHostPort(Nets.Get(b.ID).LocalNode.BAddr.String())
	pp, _ := strconv.Atoi(pport)
	bp, _ := strconv.Atoi(bport)

	errc := make(chan error, 4)
	defer close(errc)
	var wg sync.WaitGroup

	go func() {
		wg.Add(1)
		errc <- b.ListenAndServe(ctx, bp)
		wg.Done()
	}()

	go func() {
		wg.Add(1)
		errc <- b.Proxy.ListenAndServe(ctx, pp)
		wg.Done()
	}()

	go func() {
		wg.Add(1)
		errc <- b.UpdateRoot(ctx)
		wg.Done()
	}()

	go func() {
		wg.Add(1)
		errc <- Nets.Get(b.ID).TraceNodes(ctx, b)
		wg.Done()
	}()

	// read only the first message that arrives
	err := <-errc
	if ctx.Err() == nil {
		// it means that one of the rountines up here failed, but no close
		// was manually called
		b.Close()
	}

	// wait for every rountine to return before quitting
	wg.Wait()

	return err
}

// Close stops the Run routine. It drops the whole booster network, preparing for the
// node to reset or stop.
func (b *Booster) Close() error {
	log.Info.Println("booster: closing...")

	b.stop <- struct{}{}
	return nil
}

// restart restarts the Run routine.
func (b *Booster) Restart() error {
	log.Info.Println("booster: restarting...")

	Nets.Close(b.ID)
	b.restart <- struct{}{}
	return nil
}

// ListenAndServe shows to the network, listening for incoming tcp connections an
// turning them into booster connections.
func (b *Booster) ListenAndServe(ctx context.Context, port int) error {
	p := strconv.Itoa(port)
	ln, err := network.Listen("tcp", ":"+p, b.Netconfig)
	if err != nil {
		return err
	}
	defer ln.Close()

	log.Info.Printf("booster: listening on port: %v", p)

	errc := make(chan error)
	defer close(errc)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				errc <- fmt.Errorf("booster: cannot accept conn: %v", err)
				return
			}

			// send hello message first.
			if err := b.SendHello(ctx, conn); err != nil {
				errc <- err
				return
			}

			go b.Handle(ctx, conn)
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		ln.Close()
		<-errc // wait for listener to return
		return ctx.Err()
	}
}

// DialContext dials a new connection to addr and wraps the connection around
// a booster connection. Consumes the first hello message received.
func (b *Booster) DialContext(ctx context.Context, netwrk, addr string) (*Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, b.Net().DialTimeout)
	defer cancel()

	dialer := network.NewDialer(new(net.Dialer), b.Netconfig)
	conn, err := dialer.DialContext(ctx, netwrk, addr)
	if err != nil {
		return nil, err
	}

	return b.RecvHello(ctx, conn)
}

// Wire connects the target and the local node together, adding the remote booster instance
// as a new connection of the network.
func (b *Booster) Wire(ctx context.Context, network, target string) (*Conn, error) {
	// connect to the target node. The node stored in conn will not
	// trigger the tracer (i.e. ToBeTraced == false), so it is ok
	// to just close the connection in case of failure.
	conn, err := b.DialContext(ctx, network, target)
	if err != nil {
		return nil, err
	}

	fail := func(err error) (*Conn, error) {
		conn.Close()
		return nil, err
	}

	// if AddConn returns an error, chances are that the connection is
	// already present and active.
	err = b.Net().AddConn(conn)
	if err != nil {
		return fail(err)
	}

	// compose the notify packet which tells the receiver to start sending
	// information notifications when its state changes
	p, err := b.Net().EncodeDefault(nil, protocol.MessageNotify)
	if err != nil {
		return fail(err)
	}

	if err = conn.Send(p); err != nil {
		return fail(err)
	}

	log.Info.Printf("booster: -> wire: %v", target)

	// inject the heartbeat message in the connection
	p, err = b.Net().composeHeartbeat(nil)
	if err != nil {
		return fail(err)
	}
	if err = conn.Send(p); err != nil {
		return fail(err)
	}

	// start the timer that, when done, will close the connection if
	// no heartbeat message is received in time
	conn.HeartbeatTimer = time.AfterFunc(Nets.Get(b.ID).HeartbeatTTL*2, func() {
		// do not close the node multiple times.
		if conn.Conn != nil {
			log.Info.Printf("booster: no heartbeat received from conn %v: timer expired", conn.ID)
			conn.Close()
		}
	})

	// set the connection as active
	conn.RemoteNode.SetIsActive(true)
	conn.RemoteNode.ToBeTraced = true

	// handle the newly added connection in a different goroutine.
	go b.Handle(ctx, conn)

	return conn, nil
}

// UpdateRoot subscribes to the local proxy updating the root node information with the
// updated data.
func (b *Booster) UpdateRoot(ctx context.Context) error {
	errc := make(chan error)
	cancel, err := b.Proxy.Sub(&pubsub.Command{
		Topic: socks5.TopicTunnelEvents,
		Run: func(i interface{}) error {
			p, ok := i.(protocol.PayloadProxyUpdate)
			if !ok {
				return fmt.Errorf("update root: unable to recognise payload: %v", p)
			}

			node := Nets.Get(b.ID).LocalNode
			if err := b.UpdateNode(node, p, true); err != nil {
				log.Error.Printf("booster: %v", err)
			}
			return nil
		},
		PostRun: func(err error) {
			if err != nil {
				log.Error.Printf("booster: update root: %v", err)
				errc <- err
			}
		},
	})
	if err != nil {
		log.Error.Printf("booster: update root: %v", err)
		return err
	}
	defer cancel()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}

func sha1Hash(images ...[]byte) string {
	h := sha1.New()
	for _, image := range images {
		h.Write(image)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
