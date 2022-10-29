/*
 * Copyright (c) 2022, Mukama Willy Fortunate <napsterphantom@gmail.com>.
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
	"math/rand"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common/errors"
)

func StartServer(configJSON []byte) (retErr error) {

	loggingInitialized := false

	defer func() {
		if retErr != nil && loggingInitialized {
			log.WithTraceFields(LogFields{"error": retErr}).Error("RunServices failed")
		}
	}()

	rand.Seed(int64(time.Now().Nanosecond()))

	config, err := LoadConfig(configJSON)
	if err != nil {
		return errors.Trace(err)
	}

	err = InitLogging(config)
	if err != nil {
		return errors.Trace(err)
	}

	loggingInitialized = true

	dnsResolver, err := NewDNSResolver(config.DNSResolverIPAddress)
	if err != nil {
		return errors.Trace(err)
	}

	waitGroup := new(sync.WaitGroup)
	shutdownBroadcast := make(chan struct{})
	errorChannel := make(chan error, 1)

	tunnelServer, err := NewTunnelServer(config, dnsResolver, shutdownBroadcast)
	if err != nil {
		return errors.Trace(err)
	}

	waitGroup.Add(1)
	go func() {
		waitGroup.Done()
		ticker := time.NewTicker(PERIODIC_GARBAGE_COLLECTION)
		defer ticker.Stop()
		for {
			select {
			case <-shutdownBroadcast:
				return
			case <-ticker.C:
				debug.FreeOSMemory()
			}
		}
	}()

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		err := tunnelServer.Run()
		select {
		case errorChannel <- err:
		default:
		}
	}()

	// An OS signal triggers shutdown
	systemStopSignal := make(chan os.Signal, 1)
	signal.Notify(systemStopSignal, os.Interrupt, syscall.SIGTERM)

	err = nil

loop:
	for {
		select {

		case <-systemStopSignal:
			log.WithTrace().Info("shutdown by system")
			break loop

		case err = <-errorChannel:
			break loop
		}
	}

	close(shutdownBroadcast)
	waitGroup.Wait()

	return err
}
