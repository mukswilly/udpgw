/*
 * Copyright (c) 2022, Mukama Willy Fortunate.
 * All rights reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */
 
package udpgw

import (
	"fmt"
	"net"
	"sync"

	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common"
	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common/errors"
)

type TunnelServer struct {
	runWaitGroup      *sync.WaitGroup
	listenerError     chan error
	shutdownBroadcast <-chan struct{}
	udpgwServer       *udpgwServer
}

type udpgwListener struct {
	net.Listener
	localAddress string
	port         int
}

func NewTunnelServer(
	config *Config,
	dnsResolver *DNSResolver,
	shutdownBroadcast <-chan struct{}) (*TunnelServer, error) {

	udpgwServer := newUdpgwServer(config, dnsResolver, shutdownBroadcast)

	return &TunnelServer{
		runWaitGroup:      new(sync.WaitGroup),
		listenerError:     make(chan error),
		shutdownBroadcast: shutdownBroadcast,
		udpgwServer:       udpgwServer,
	}, nil
}

func (server *TunnelServer) Run() error {

	listenPort := server.udpgwServer.config.UdpgwPort
	localAddress := fmt.Sprintf("%s:%d", "127.0.0.1", listenPort)

	ln, err := net.Listen("tcp", localAddress)
	if err != nil {
		return errors.Trace(err)
	}

	listener := &udpgwListener{
		Listener:     ln,
		localAddress: localAddress,
		port:         int(listenPort),
	}

	log.WithTraceFields(
		LogFields{
			"localAddress": localAddress,
		}).Info("listening")

	server.runWaitGroup.Add(1)
	go func(listener *udpgwListener) {
		defer server.runWaitGroup.Done()

		server.udpgwServer.runListener(
			listener,
			server.listenerError)

		log.WithTraceFields(
			LogFields{
				"localAddress": listener.localAddress,
			}).Info("stopped")

	}(listener)

	select {
	case <-server.shutdownBroadcast:
	case err = <-server.listenerError:
	}

	listener.Close()
	server.udpgwServer.stopClients()
	server.runWaitGroup.Wait()

	log.WithTrace().Info("stopped")

	return err
}

type udpgwServer struct {
	config            *Config
	dnsResolver       *DNSResolver
	shutdownBroadcast <-chan struct{}
	connsMutex        sync.Mutex
	conns             *common.Conns
}

func newUdpgwServer(
	config *Config,
	dnsResolver *DNSResolver,
	shutdownBroadcast <-chan struct{}) *udpgwServer {

	return &udpgwServer{
		config:            config,
		dnsResolver:       dnsResolver,
		shutdownBroadcast: shutdownBroadcast,
		conns:             common.NewConns(),
	}
}

// runListener is intended to run an a goroutine; it blocks
// running the udpgw listener. If an unrecoverable error
// occurs, it will send the error to the listenerError channel.
func (udpgwServer *udpgwServer) runListener(udpgwListener *udpgwListener, listenerError chan<- error) {

	handleClient := func(clientConn net.Conn) {

		// Process each client connection concurrently.
		go udpgwServer.handleClient(clientConn)
	}

	for {
		conn, err := udpgwListener.Listener.Accept()

		select {
		case <-udpgwServer.shutdownBroadcast:
			if err == nil {
				conn.Close()
			}
			return
		default:
		}

		if err != nil {
			if e, ok := err.(net.Error); ok && e.Temporary() {
				log.WithTraceFields(LogFields{"error": err}).Error("accept failed")
				// Temporary error, keep running
				continue
			}

			select {
			case listenerError <- errors.Trace(err):
			default:
			}
			return
		}

		handleClient(conn)
	}
}

func (udpgwServer *udpgwServer) handleClient(conn net.Conn) {
	udpgwServer.conns.Add(conn)
	defer udpgwServer.conns.Remove(conn)

	udpgwServer.handleUdpgw(conn)
}

func (udpgwServer *udpgwServer) stopClients() {

	udpgwServer.connsMutex.Lock()
	conns := udpgwServer.conns
	udpgwServer.conns = nil
	udpgwServer.connsMutex.Unlock()

	conns.CloseAll()
}
