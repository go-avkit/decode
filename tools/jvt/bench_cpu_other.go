// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

//go:build !unix

package main

import "time"

// cpuTime has no portable source here (no getrusage), so it counts wall time:
// the bench's CPU ratio then equals its time ratio.
func cpuTime() time.Duration { return time.Duration(time.Now().UnixNano()) }
