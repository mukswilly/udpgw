// +build windows

package main

import (
	"os"
)

var signals = []os.Signal{os.Interrupt, os.Kill}