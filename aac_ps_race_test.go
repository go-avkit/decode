// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

//go:build race

package decode

// psRaceBuild: the race detector changes the fused steps of the arm64 build.
const psRaceBuild = true
