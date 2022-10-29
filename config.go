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
	"encoding/json"
	"os"
	"time"

	"github.com/Psiphon-Labs/psiphon-tunnel-core/psiphon/common/errors"
)

const (
	DEFAULT_LOG_FILE_REOPEN_RETRIES = 25
	PERIODIC_GARBAGE_COLLECTION     = 120 * time.Second
	DEFAULT_UDPGW_PORT              = 7300
	DEFAULT_CONFIG_FILE_NAME        = "udpgw.json"
)

// Config specifies the configuration and behavior of a Udpgw
// server.
type Config struct {

	// LogLevel specifies the log level. Valid values are:
	// panic, fatal, error, warn, info, debug
	LogLevel string

	// LogFilename specifies the path of the file to log
	// to. When blank, logs are written to stderr.
	LogFilename string

	// LogFileReopenRetries specifies how many retries, each with a 1ms delay,
	// will be attempted after reopening a rotated log file fails. Retries
	// mitigate any race conditions between writes/reopens and file operations
	// performed by external log managers, such as logrotate.
	//
	// When omitted, DEFAULT_LOG_FILE_REOPEN_RETRIES is used.
	LogFileReopenRetries *int

	// LogFileCreateMode specifies that the Psiphon server should create a new
	// log file when one is not found, such as after rotation with logrotate
	// configured with nocreate. The value is the os.FileMode value to use when
	// creating the file.
	//
	// When omitted, the Psiphon server does not create log files.
	LogFileCreateMode *int

	// SkipPanickingLogWriter disables panicking when
	// unable to write any logs.
	SkipPanickingLogWriter bool

	// HostID is the ID of the server host; this is used for
	// event logging.
	HostID string

	// UdpgwPort is the port at which the udpgw server should run.
	UdpgwPort uint16

	// DNSResolverIPAddress specifies the IP address of a DNS server
	// to be used when "/etc/resolv.conf" doesn't exist or fails to
	// parse. When blank, "/etc/resolv.conf" must contain a usable
	// "nameserver" entry.
	DNSResolverIPAddress string
}

// GetLogFileReopenConfig gets the reopen retries, and create/mode inputs for
// rotate.NewRotatableFileWriter, which is used when writing to log files.
//
// By default, we expect the log files to be managed by logrotate, with
// logrotate configured to re-create the next log file after rotation. As
// described in the documentation for rotate.NewRotatableFileWriter, and as
// observed in production, we occasionally need retries when attempting to
// reopen the log file post-rotation; and we avoid conflicts, and spurious
// re-rotations, by disabling file create in rotate.NewRotatableFileWriter. In
// large scale production, incidents requiring retry are very rare, so the
// retry delay is not expected to have a significant impact on performance.
//
// The defaults may be overriden in the Config.
func (config *Config) GetLogFileReopenConfig() (int, bool, os.FileMode) {

	retries := DEFAULT_LOG_FILE_REOPEN_RETRIES
	if config.LogFileReopenRetries != nil {
		retries = *config.LogFileReopenRetries
	}
	create := false
	mode := os.FileMode(0)
	if config.LogFileCreateMode != nil {
		create = true
		mode = os.FileMode(*config.LogFileCreateMode)
	}
	return retries, create, mode
}

// LoadConfig loads and validates a JSON encoded server config.
func LoadConfig(configJSON []byte) (*Config, error) {

	var config Config
	err := json.Unmarshal(configJSON, &config)
	if err != nil {
		return nil, errors.Trace(err)
	}

	if config.UdpgwPort == 0 {
		return nil, errors.TraceNew("UdpgwPort is required")
	}

	return &config, nil
}

// GenerateConfigParams specifies customizations to be applied to
// a generated server config.
type GenerateConfigParams struct {
	LogFilename            string
	SkipPanickingLogWriter bool
	LogLevel               string
	UdpgwPort              uint16
}

func GenerateConfig(params *GenerateConfigParams) ([]byte, error) {
	if params.UdpgwPort == 0 {
		return nil, errors.TraceNew("invalid Udpgw port")
	}

	logLevel := params.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}

	createMode := 0666

	config := &Config{
		LogLevel:               logLevel,
		LogFilename:            params.LogFilename,
		LogFileCreateMode:      &createMode,
		SkipPanickingLogWriter: params.SkipPanickingLogWriter,
		HostID:                 "example-host-id",
		UdpgwPort:              params.UdpgwPort,
		DNSResolverIPAddress:   "8.8.8.8",
	}

	encodedConfig, err := json.MarshalIndent(config, "\n", "    ")
	if err != nil {
		return nil, errors.Trace(err)
	}

	return encodedConfig, nil
}
