// +build !windows

package main

import (
	"os"
	"syscall"
)

var signals = []os.Signal{os.Interrupt, os.Kill, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTSTP, syscall.SIGCONT}