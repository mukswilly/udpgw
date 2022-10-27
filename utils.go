/*
 * Copyright (c) 2016, Psiphon Inc.
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
	"io"

	"github.com/miekg/dns"
)

// IntentionalPanicError is an error type that is used
// when calling panic() in a situation where recovers
// should propagate the panic.
type IntentionalPanicError struct {
	message string
}

// NewIntentionalPanicError creates a new IntentionalPanicError.
func NewIntentionalPanicError(errorMessage string) error {
	return IntentionalPanicError{
		message: fmt.Sprintf("intentional panic error: %s", errorMessage)}
}

// Error implements the error interface.
func (err IntentionalPanicError) Error() string {
	return err.message
}

// PanickingLogWriter wraps an io.Writer and intentionally
// panics when a Write() fails.
type PanickingLogWriter struct {
	name   string
	writer io.Writer
}

// NewPanickingLogWriter creates a new PanickingLogWriter.
func NewPanickingLogWriter(
	name string, writer io.Writer) *PanickingLogWriter {

	return &PanickingLogWriter{
		name:   name,
		writer: writer,
	}
}

// Write implements the io.Writer interface.
func (w *PanickingLogWriter) Write(p []byte) (n int, err error) {
	n, err = w.writer.Write(p)
	if err != nil {
		panic(
			NewIntentionalPanicError(
				fmt.Sprintf("fatal write to %s failed: %s", w.name, err)))
	}
	return
}

// parseDNSQuestion parses a DNS message. When the message is a query,
// the first question, a fully-qualified domain name, is returned.
//
// For other valid DNS messages, "" is returned. An error is returned only
// for invalid DNS messages.
//
// Limitations:
//   - Only the first Question field is extracted.
//   - ParseDNSQuestion only functions for plaintext DNS and cannot
//     extract domains from DNS-over-TLS/HTTPS, etc.
func parseDNSQuestion(request []byte) (string, error) {
	m := new(dns.Msg)
	err := m.Unpack(request)
	if err != nil {
		return "", err
	}
	if len(m.Question) > 0 {
		return m.Question[0].Name, nil
	}
	return "", nil
}
