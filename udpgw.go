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
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common/errors"
)

// This is based on https://github.com/Psiphon-Labs/psiphon-tunnel-core/blob/b1e84c4866c1568f380309f73ebc569fdde96652/psiphon/server/udp.go
// with a few modifications to suit this project

// handleUdpgw implements UDP port forwarding. A single client
// conn follows the udpgw protocol, which multiplexes many
// UDP port forwards.
//
// The udpgw protocol and original server implementation:
// Copyright (c) 2009, Ambroz Bizjak <ambrop7@gmail.com>
// https://github.com/ambrop72/badvpn
func (udpgwServer *udpgwServer) handleUdpgw(clientConn net.Conn) {
	defer clientConn.Close()

	multiplexer := &udpgwPortForwardMultiplexer{
		clientConn:     clientConn,
		dnsResolver:    udpgwServer.dnsResolver,
		portForwards:   make(map[uint16]*udpgwPortForward),
		relayWaitGroup: new(sync.WaitGroup),
		runWaitGroup:   new(sync.WaitGroup),
	}

	multiplexer.runWaitGroup.Add(1)
	multiplexer.run()
	multiplexer.runWaitGroup.Done()
}

type udpgwPortForwardMultiplexer struct {
	clientConnWriteMutex sync.Mutex
	clientConn           net.Conn
	dnsResolver          *DNSResolver
	portForwardsMutex    sync.Mutex
	portForwards         map[uint16]*udpgwPortForward
	relayWaitGroup       *sync.WaitGroup
	runWaitGroup         *sync.WaitGroup
}

func (mux *udpgwPortForwardMultiplexer) run() {

	// In a loop, read udpgw messages from the client. Each
	// message contains a UDP packet to send upstream either via a new port
	// forward, or on an existing port forward.
	//
	// A goroutine is run to read downstream packets for each UDP port forward. All read
	// packets are encapsulated in udpgw protocol and sent down to the client.
	//
	// When the client disconnects or the server shuts down, the client conn will close and
	// readUdpgwMessage will exit with EOF.

	buffer := make([]byte, udpgwProtocolMaxMessageSize)
	for {
		// Note: message.packet points to the reusable memory in "buffer".
		// Each readUdpgwMessage call will overwrite the last message.packet.
		message, err := readUdpgwMessage(mux.clientConn, buffer)
		if err != nil {
			if err != io.EOF {
				// Debug since I/O errors occur during normal operation
				log.WithTraceFields(LogFields{"error": err}).Debug("readUdpgwMessage failed")
			}
			break
		}

		mux.portForwardsMutex.Lock()
		portForward := mux.portForwards[message.connID]
		mux.portForwardsMutex.Unlock()

		// In the udpgw protocol, an existing port forward is closed when
		// either the discard flag is set or the remote address has changed.

		if portForward != nil &&
			(message.discardExistingConn ||
				!bytes.Equal(portForward.remoteIP, message.remoteIP) ||
				portForward.remotePort != message.remotePort) {

			// The port forward's goroutine will complete cleanup
			// portForward.conn.Close() will signal this shutdown.
			portForward.tunnelConn.Close()

			// Synchronously await the termination of the relayDownstream
			// goroutine. This ensures that the previous goroutine won't
			// invoke removePortForward, with the connID that will be reused
			// for the new port forward, after this point.
			//
			// Limitation: this synchronous shutdown cannot prevent a "wrong
			// remote address" error on the badvpn udpgw client, which occurs
			// when the client recycles a port forward (setting discard) but
			// receives, from the server, a udpgw message containing the old
			// remote address for the previous port forward with the same
			// conn ID. That downstream message from the server may be in
			// flight in the SSH channel when the client discard message arrives.
			portForward.relayWaitGroup.Wait()

			portForward = nil
		}

		if portForward == nil {

			// Create a new port forward

			dialIP := net.IP(message.remoteIP)
			dialPort := int(message.remotePort)

			// Validate DNS packets when the client
			// indicates DNS or when DNS is _not_ indicated and the destination port is
			// 53.
			if message.forwardDNS || message.remotePort == 53 {

				_, err := parseDNSQuestion(message.packet)
				if err != nil {
					log.WithTraceFields(LogFields{"error": err}).Debug("ParseDNSQuestion failed")
					// Drop packet
					continue
				}
			}

			if message.forwardDNS {
				// Transparent DNS forwarding. In this case, isPortForwardPermitted
				// traffic rules checks are bypassed, since DNS is essential.
				dialIP = mux.dnsResolver.Get()
				dialPort = DNS_RESOLVER_PORT
			}

			// Note: UDP port forward counting has no dialing phase

			// Pre-check log level to avoid overhead of rendering log for
			// every DNS query and other UDP port forward.
			if IsLogLevelDebug() {
				log.WithTraceFields(
					LogFields{
						"remoteAddr": net.JoinHostPort(dialIP.String(), strconv.Itoa(dialPort)),
						"connID":     message.connID}).Debug("dialing")
			}

			udpConn, err := net.DialUDP(
				"udp", nil, &net.UDPAddr{IP: dialIP, Port: dialPort})
			if err != nil {
				log.WithTraceFields(LogFields{"error": err}).Debug("DialUDP failed")
				continue
			}

			portForward = &udpgwPortForward{
				connID:         message.connID,
				preambleSize:   message.preambleSize,
				remoteIP:       message.remoteIP,
				remotePort:     message.remotePort,
				dialIP:         dialIP,
				tunnelConn:     udpConn,
				bytesUp:        0,
				bytesDown:      0,
				relayWaitGroup: new(sync.WaitGroup),
				mux:            mux,
			}

			mux.portForwardsMutex.Lock()
			mux.portForwards[portForward.connID] = portForward
			mux.portForwardsMutex.Unlock()

			portForward.relayWaitGroup.Add(1)
			mux.relayWaitGroup.Add(1)
			go portForward.relayDownstream()
		}

		// Note: assumes UDP writes won't block (https://golang.org/pkg/net/#UDPConn.WriteToUDP)
		_, err = portForward.tunnelConn.Write(message.packet)
		if err != nil {
			log.WithTraceFields(LogFields{"error": err}).Debug("upstream UDP relay failed")
			// The port forward's goroutine will complete cleanup
			portForward.tunnelConn.Close()
		}

		atomic.AddInt64(&portForward.bytesUp, int64(len(message.packet)))
	}

	// Cleanup all udpgw port forward workers when exiting

	mux.portForwardsMutex.Lock()
	for _, portForward := range mux.portForwards {
		// The port forward's goroutine will complete cleanup
		portForward.tunnelConn.Close()
	}
	mux.portForwardsMutex.Unlock()

	mux.relayWaitGroup.Wait()
}

func (mux *udpgwPortForwardMultiplexer) removePortForward(connID uint16) {
	mux.portForwardsMutex.Lock()
	delete(mux.portForwards, connID)
	mux.portForwardsMutex.Unlock()
}

type udpgwPortForward struct {
	// Note: 64-bit ints used with atomic operations are placed
	// at the start of struct to ensure 64-bit alignment.
	// (https://golang.org/pkg/sync/atomic/#pkg-note-BUG)

	bytesUp        int64
	bytesDown      int64
	connID         uint16
	preambleSize   int
	remoteIP       []byte
	remotePort     uint16
	dialIP         net.IP
	tunnelConn     net.Conn
	relayWaitGroup *sync.WaitGroup
	mux            *udpgwPortForwardMultiplexer
}

func (portForward *udpgwPortForward) relayDownstream() {
	defer portForward.relayWaitGroup.Done()
	defer portForward.mux.relayWaitGroup.Done()

	// Downstream UDP packets are read into the reusable memory
	// in "buffer" starting at the offset past the udpgw message
	// header and address, leaving enough space to write the udpgw
	// values into the same buffer and use for writing to the client conn.
	buffer := make([]byte, udpgwProtocolMaxMessageSize)
	packetBuffer := buffer[portForward.preambleSize:udpgwProtocolMaxMessageSize]
	for {
		// TODO: if read buffer is too small, excess bytes are discarded?
		packetSize, err := portForward.tunnelConn.Read(packetBuffer)
		if packetSize > udpgwProtocolMaxPayloadSize {
			err = fmt.Errorf("unexpected packet size: %d", packetSize)
		}
		if err != nil {
			if err != io.EOF {
				// Debug since errors such as "use of closed network connection" occur during normal operation
				log.WithTraceFields(LogFields{"error": err}).Debug("downstream UDP relay failed")
			}
			break
		}

		err = writeUdpgwPreamble(
			portForward.preambleSize,
			0,
			portForward.connID,
			portForward.remoteIP,
			portForward.remotePort,
			uint16(packetSize),
			buffer)
		if err == nil {
			portForward.mux.clientConnWriteMutex.Lock()
			_, err = portForward.mux.clientConn.Write(buffer[0 : portForward.preambleSize+packetSize])
			portForward.mux.clientConnWriteMutex.Unlock()
		}

		if err != nil {
			// Close the client conn, which will interrupt the main loop.
			portForward.mux.clientConn.Close()
			log.WithTraceFields(LogFields{"error": err}).Debug("downstream UDP relay failed")
			break
		}

		atomic.AddInt64(&portForward.bytesDown, int64(packetSize))
	}

	portForward.mux.removePortForward(portForward.connID)

	portForward.tunnelConn.Close()

	bytesUp := atomic.LoadInt64(&portForward.bytesUp)
	bytesDown := atomic.LoadInt64(&portForward.bytesDown)

	log.WithTraceFields(
		LogFields{
			"remoteAddr": net.JoinHostPort(
				net.IP(portForward.remoteIP).String(), strconv.Itoa(int(portForward.remotePort))),
			"bytesUp":   bytesUp,
			"bytesDown": bytesDown,
			"connID":    portForward.connID}).Debug("exiting")
}

// TODO: express and/or calculate udpgwProtocolMaxPayloadSize as function of MTU?
const (
	udpgwProtocolFlagKeepalive = 1 << 0
	udpgwProtocolFlagRebind    = 1 << 1
	udpgwProtocolFlagDNS       = 1 << 2
	udpgwProtocolFlagIPv6      = 1 << 3

	udpgwProtocolMaxPreambleSize = 23
	udpgwProtocolMaxPayloadSize  = 32768
	udpgwProtocolMaxMessageSize  = udpgwProtocolMaxPreambleSize + udpgwProtocolMaxPayloadSize
)

type udpgwProtocolMessage struct {
	connID              uint16
	preambleSize        int
	remoteIP            []byte
	remotePort          uint16
	discardExistingConn bool
	forwardDNS          bool
	packet              []byte
}

func readUdpgwMessage(
	reader io.Reader, buffer []byte) (*udpgwProtocolMessage, error) {

	// udpgw message layout:
	//
	// | 2 byte size | 3 byte header | 6 or 18 byte address | variable length packet |

	for {
		// Read message

		_, err := io.ReadFull(reader, buffer[0:2])
		if err != nil {
			if err != io.EOF {
				err = errors.Trace(err)
			}
			return nil, err
		}

		size := binary.LittleEndian.Uint16(buffer[0:2])

		if size < 3 || int(size) > len(buffer)-2 {
			return nil, errors.TraceNew("invalid udpgw message size")
		}

		_, err = io.ReadFull(reader, buffer[2:2+size])
		if err != nil {
			if err != io.EOF {
				err = errors.Trace(err)
			}
			return nil, err
		}

		flags := buffer[2]

		connID := binary.LittleEndian.Uint16(buffer[3:5])

		// Ignore udpgw keep-alive messages -- read another message

		if flags&udpgwProtocolFlagKeepalive == udpgwProtocolFlagKeepalive {
			continue
		}

		// Read address

		var remoteIP []byte
		var remotePort uint16
		var packetStart, packetEnd int

		if flags&udpgwProtocolFlagIPv6 == udpgwProtocolFlagIPv6 {

			if size < 21 {
				return nil, errors.TraceNew("invalid udpgw message size")
			}

			remoteIP = make([]byte, 16)
			copy(remoteIP, buffer[5:21])
			remotePort = binary.BigEndian.Uint16(buffer[21:23])
			packetStart = 23
			packetEnd = 23 + int(size) - 21

		} else {

			if size < 9 {
				return nil, errors.TraceNew("invalid udpgw message size")
			}

			remoteIP = make([]byte, 4)
			copy(remoteIP, buffer[5:9])
			remotePort = binary.BigEndian.Uint16(buffer[9:11])
			packetStart = 11
			packetEnd = 11 + int(size) - 9
		}

		// Assemble message
		// Note: udpgwProtocolMessage.packet references memory in the input buffer

		message := &udpgwProtocolMessage{
			connID:              connID,
			preambleSize:        packetStart,
			remoteIP:            remoteIP,
			remotePort:          remotePort,
			discardExistingConn: flags&udpgwProtocolFlagRebind == udpgwProtocolFlagRebind,
			forwardDNS:          flags&udpgwProtocolFlagDNS == udpgwProtocolFlagDNS,
			packet:              buffer[packetStart:packetEnd],
		}

		return message, nil
	}
}

func writeUdpgwPreamble(
	preambleSize int,
	flags uint8,
	connID uint16,
	remoteIP []byte,
	remotePort uint16,
	packetSize uint16,
	buffer []byte) error {

	if preambleSize != 7+len(remoteIP) {
		return errors.TraceNew("invalid udpgw preamble size")
	}

	size := uint16(preambleSize-2) + packetSize

	// size
	binary.LittleEndian.PutUint16(buffer[0:2], size)

	// flags
	buffer[2] = flags

	// connID
	binary.LittleEndian.PutUint16(buffer[3:5], connID)

	// addr
	copy(buffer[5:5+len(remoteIP)], remoteIP)
	binary.BigEndian.PutUint16(buffer[5+len(remoteIP):7+len(remoteIP)], remotePort)

	return nil
}
