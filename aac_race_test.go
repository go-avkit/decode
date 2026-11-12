// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

//go:build race

package decode

// raceEnabled: under the race detector sync.Pool drops items at random (to expose
// code that relies on getting one back), so a transform whose scratch lives in a pool
// allocates, and an allocation count says nothing about the code.
const raceEnabled = true
