// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

//go:build unix

package main

import (
	"syscall"
	"time"
)

// cpuTime is this process's user plus system CPU time so far.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru) // RUSAGE_SELF cannot fail
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
