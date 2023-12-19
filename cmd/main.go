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

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/Psiphon-Inc/rotate-safe-writer"
	"github.com/mitchellh/panicwrap"
	"github.com/ZhymabekRoman/udpgw-windows"
)

// This code is inspired by https://github.com/Psiphon-Labs/psiphon-tunnel-core/blob/b1e84c4866c1568f380309f73ebc569fdde96652/Server/main.go

var loadedConfigJSON []byte

func main() {
	var configFilename string
	var generateUdpgwPort int
	var generateLogFilename string

	flag.StringVar(
		&configFilename,
		"config",
		udpgw.DEFAULT_CONFIG_FILE_NAME,
		"run or generate with this config `filename`")

	flag.IntVar(
		&generateUdpgwPort,
		"port",
		udpgw.DEFAULT_UDPGW_PORT,
		"generate with this udpgw port")

	flag.StringVar(
		&generateLogFilename,
		"logFilename",
		"",
		"set application log file name and path; blank for stderr")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"Usage:\n\n"+
				"%s <flags> generate    generates configuration files\n"+
				"%s <flags> run         runs configured services\n\n",
			os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	} else if args[0] == "generate" {

		udpgwPort := generateUdpgwPort

		configJSON, err :=
			udpgw.GenerateConfig(
				&udpgw.GenerateConfigParams{
					LogFilename: generateLogFilename,
					UdpgwPort:   uint16(udpgwPort),
				})
		if err != nil {
			fmt.Printf("generate failed: %s\n", err)
			os.Exit(1)
		}

		fileMode := os.FileMode(0600)
		if runtime.GOOS == "windows" {
			fileMode = 0
		}

		err = os.WriteFile(configFilename, configJSON, fileMode)
		if err != nil {
			fmt.Printf("error writing configuration file: %s\n", err)
			os.Exit(1)
		}

	} else if args[0] == "run" {
		configJSON, err := os.ReadFile(configFilename)
		if err != nil {
			fmt.Printf("error loading configuration file: %s\n", err)
			os.Exit(1)
		}

		loadedConfigJSON = configJSON

		// The initial call to panicwrap.Wrap will spawn a child process
		// running the same program.
		//
		// The parent process waits for the child to terminate and
		// panicHandler logs any panics from the child.
		//
		// The child will return immediately from Wrap without spawning
		// and fall through to server.RunServices.

		// Unhandled panic wrapper. Logs it, then re-executes the current executable
		if runtime.GOOS == "windows" {
			exitStatus, err := panicwrap.Wrap(&panicwrap.WrapConfig{
				Handler:        panicHandler,
				ForwardSignals: []os.Signal{os.Interrupt, os.Kill},
			})
		} else {
			exitStatus, err := panicwrap.Wrap(&panicwrap.WrapConfig{
				Handler:        panicHandler,
				ForwardSignals: []os.Signal{os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTSTP, syscall.SIGCONT},
			})
		}
		if err != nil {
			fmt.Printf("failed to set up the panic wrapper: %s\n", err)
			os.Exit(1)
		}

		// Note: panicwrap.Wrap documentation states that exitStatus == -1
		// should be used to determine whether the process is the child.
		// However, we have found that this exitStatus is returned even when
		// the process is the parent. Likely due to panicwrap returning
		// syscall.WaitStatus.ExitStatus() as the exitStatus, which _can_ be
		// -1. Checking panicwrap.Wrapped(nil) is more reliable.

		if !panicwrap.Wrapped(nil) {
			os.Exit(exitStatus)
		}
		// Else, this is the child process.

		err = udpgw.StartServer(configJSON)
		if err != nil {
			fmt.Printf("run failed: %s\n", err)
			os.Exit(1)
		}
	}
}

func panicHandler(output string) {
	if len(loadedConfigJSON) > 0 {
		config, err := udpgw.LoadConfig([]byte(loadedConfigJSON))
		if err != nil {
			fmt.Printf("error parsing configuration file: %s\n%s\n", err, output)
			os.Exit(1)
		}

		logEvent := make(map[string]string)
		logEvent["host_id"] = config.HostID
		logEvent["timestamp"] = time.Now().Format(time.RFC3339)
		logEvent["event_name"] = "server_panic"

		// ELK has a maximum field length of 32766 UTF8 bytes. Over that length, the
		// log won't be delivered. Truncate the panic field, as it may be much longer.
		maxLen := 32766
		if len(output) > maxLen {
			output = output[:maxLen]
		}

		logEvent["panic"] = output

		// Logs are written to the configured file name. If no name is specified, logs are written to stderr
		var jsonWriter io.Writer
		if config.LogFilename != "" {

			retries, create, mode := config.GetLogFileReopenConfig()
			panicLog, err := rotate.NewRotatableFileWriter(
				config.LogFilename, retries, create, mode)
			if err != nil {
				fmt.Printf("unable to set panic log output: %s\n%s\n", err, output)
				os.Exit(1)
			}
			defer panicLog.Close()

			jsonWriter = panicLog
		} else {
			jsonWriter = os.Stderr
		}

		enc := json.NewEncoder(jsonWriter)
		err = enc.Encode(logEvent)
		if err != nil {
			fmt.Printf("unable to serialize panic message to JSON: %s\n%s\n", err, output)
			os.Exit(1)
		}
	} else {
		fmt.Printf("no configuration JSON was loaded, cannot continue\n%s\n", output)
		os.Exit(1)
	}

	os.Exit(1)
}
