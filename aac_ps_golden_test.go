// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/cpu"
)

// psOutputSHA is the SHA-256 of the left then the right PCM, as float32 bits.
func psOutputSHA(l, r []float32) string {
	h := sha256.New()
	var b [4]byte
	for _, ch := range [2][]float32{l, r} {
		for _, v := range ch {
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
			h.Write(b[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// psGoldenArm64 and psGoldenAmd64 are the SHA-256 of the decoded PCM of every written stream
// (S01 to S19, which exercise the layout switch, the freeze and resume, the carried matrix
// and the fade in of the first matrix) and of he44-ps.aac, recorded from the decoder before
// the PS stages were restructured to run slot by slot. They pin the output bit for bit: a
// restructuring that keeps the arithmetic of every sample (the order of the additions, the
// fused multiply-adds) leaves them unchanged. The bits depend on the architecture (Go fuses
// multiply-adds on arm64, not on amd64, and the DCT pair kernels differ), so there is one
// table per architecture; amd64 is the one with AVX2 and FMA, which every CI runner has. A
// change of the arithmetic on purpose records them again and says why.
var psGoldenArm64 = map[string]string{
	"S01":         "52875a22e6af64ce6039f4910330297556ac9998fe43d3569e18b73149913ebc",
	"S02":         "cbeb7be081926a6174a1844d02bac7455e70c9ec8947840b35c4d1759d841029",
	"S03":         "52875a22e6af64ce6039f4910330297556ac9998fe43d3569e18b73149913ebc",
	"S04":         "562fb294ba9f75c60c9ac396d6c4a397dd6107a309310d309e37b579a85d3753",
	"S05":         "4ff39dd4b58558b1c8f617227a447a227b035d628395d6df09dd4d06962856ce",
	"S06":         "9b4bc697ec4e1bf3619e6bc874e91ef90c6638f8dac75636ca67fcc46f517f9c",
	"S06b":        "8852c0ffbf6318a42e1d28cedbc6178414ddb843f4d7c23c41778fadcf3a9aa9",
	"S07":         "e9d0c4603f8c9294b259a81c8d5d153bbaec9899f2ada60cf87d8bf79e05e6f0",
	"S08":         "fd4a84d5882c46b313436ce5dd18197133814e3dbc378fde17a590a462b7d9a1",
	"S09c":        "d4eb679f100662d02612315c66b260a43387edeb984f5b9c8d90703b9af4334d",
	"S09f":        "96bafc2731b6724a2caed630ad9a3a9c406ded439c7e2cc7940a1c977aac37fd",
	"S10":         "57b3540b2038ea00832587ddd0d675d6af361efc8f774d032fd098fc2e12f72b",
	"S11":         "d6774f090e42dcc60693c4a4c0aff9c75099fa8eb5001d8eae706a5755fd935d",
	"S12":         "416b6d73f586c5f8b95f2a516d6f0d8ecd4eb7b7b4ad934982e6a43256af8a80",
	"S13a":        "67bbc5d9995bcb28c87ddeba86d7c09bb818dd4751d96e130f5ec8e5d09650fa",
	"S13b":        "99c11665b06a7f09a42a5ecfb5d50750a02533b6c50a5b5527d6611b2157e9b3",
	"S14a":        "b739da82b6f9b2d99f57a3b20a4de44f69d37b8b62c1f307c25c0580a0754211",
	"S14b":        "5100fd25b48aabfb7b7ea11fdac6fa56affa8e51a22d476eda1f533943bd9c6b",
	"S14c":        "a5f5a978a7daa49ee9cce03f58d4302be9c97342be3f9fd27f526594afb643b6",
	"S15":         "d204c794e90cbf9de7f5d255b25ef6ed7f4b53428390445d033f2c395193af59",
	"S16a20":      "aefabcb1f9980a51501a81b6f14b0c129215dd4b7f31c6270c58f8b11b90d1b8",
	"S16a34":      "a9d98f87df079c08eab2d36636547242a75261b2bf96b5b07fbbf0bab60aa50d",
	"S16b20":      "b28e74551ce7fd090641b1674cfcd6f98b72cf5a3c696af8008c90e0fca25fc6",
	"S16b34":      "0e2a8ae55de11fa62fa5ccd44f86bb613b3fdf924ab6fbe8680900cec23eb1fa",
	"S16c20":      "384c7afa92f0b9698e1a334d6e4170ce8662ceec4749a82a0dbe9d4cbed51120",
	"S16c34":      "c1e08d1b21fed21c26ecf37a2bc99b816b7f2e156aeaa1dc7dbb981f7a7365f1",
	"S16f":        "3137ce21004e79ecc03d4a757870d7dff85e7a63df2183ccad301e212e682e1f",
	"S16g":        "e756fa971f07396860ba0ad25492af79aa018034d04f9f3993d62ec3518f4b4b",
	"S16h":        "7dd806545f6dda776d92d0691028427b22e0f8620bcc8689d343aa957a009232",
	"S16d":        "e2a19e5a3f502e7ada1282659cbced31afb3ff37f630945157bea64859817795",
	"S16e":        "bd9dee0c692989ad291e0ecf717d5410766e81ad671ee718c79c54cdd3a3f821",
	"S17b":        "62ea42b05adce42307f3f8ed6d5a271d050b3fc54b727d15180471b8fab798f6",
	"S17":         "39b5431b540597e25ee62c3381169dcdd7cbf1371963b2f9f9dc4362b53c76f4",
	"S18":         "98fe6c9ae1789a35f6a8f0b3e95942044cf7bac7a5333679d11253605560f6aa",
	"S18b":        "405b9361ebebec221106cabc226d17da2844d35340cfbd4ad7a65473e91ede06",
	"S18c":        "b6439b0f025133bdf214cb896eca68f3baf315ca7fa8b76ca8d590e0105c7b86",
	"S18d":        "87c448db9a7ff846f6d29475580641a526163506dea50631228fec221f927629",
	"S18e":        "db171e2ae59093860b25e25c5dfc2ce40fd8c56a8945d7696f687f72b27652c2",
	"S19c":        "a95f23d0d967628328f2459421004b928948fc445bc993c7c1474f0993af3f0f",
	"S19a":        "150ef6495a80f79dcbf25b72a9332f2500991181d9cf240c0fdf7a0ab64261a6",
	"S19b":        "f0e4b773e100d65589f963db8ed9ef977b6991f2fb184086b3e11a144d6f5238",
	"he44-ps.aac": "9a40938beaeb1a033458670a1976943644bf0442088f3ba1057ccf9bb85b17f6",
}

var psGoldenAmd64 = map[string]string{
	"S01":         "52875a22e6af64ce6039f4910330297556ac9998fe43d3569e18b73149913ebc",
	"S02":         "cbeb7be081926a6174a1844d02bac7455e70c9ec8947840b35c4d1759d841029",
	"S03":         "52875a22e6af64ce6039f4910330297556ac9998fe43d3569e18b73149913ebc",
	"S04":         "562fb294ba9f75c60c9ac396d6c4a397dd6107a309310d309e37b579a85d3753",
	"S05":         "4ff39dd4b58558b1c8f617227a447a227b035d628395d6df09dd4d06962856ce",
	"S06":         "9b4bc697ec4e1bf3619e6bc874e91ef90c6638f8dac75636ca67fcc46f517f9c",
	"S06b":        "8852c0ffbf6318a42e1d28cedbc6178414ddb843f4d7c23c41778fadcf3a9aa9",
	"S07":         "e9d0c4603f8c9294b259a81c8d5d153bbaec9899f2ada60cf87d8bf79e05e6f0",
	"S08":         "fd4a84d5882c46b313436ce5dd18197133814e3dbc378fde17a590a462b7d9a1",
	"S09c":        "d4eb679f100662d02612315c66b260a43387edeb984f5b9c8d90703b9af4334d",
	"S09f":        "96bafc2731b6724a2caed630ad9a3a9c406ded439c7e2cc7940a1c977aac37fd",
	"S10":         "57b3540b2038ea00832587ddd0d675d6af361efc8f774d032fd098fc2e12f72b",
	"S11":         "d6774f090e42dcc60693c4a4c0aff9c75099fa8eb5001d8eae706a5755fd935d",
	"S12":         "416b6d73f586c5f8b95f2a516d6f0d8ecd4eb7b7b4ad934982e6a43256af8a80",
	"S13a":        "67bbc5d9995bcb28c87ddeba86d7c09bb818dd4751d96e130f5ec8e5d09650fa",
	"S13b":        "99c11665b06a7f09a42a5ecfb5d50750a02533b6c50a5b5527d6611b2157e9b3",
	"S14a":        "b739da82b6f9b2d99f57a3b20a4de44f69d37b8b62c1f307c25c0580a0754211",
	"S14b":        "5100fd25b48aabfb7b7ea11fdac6fa56affa8e51a22d476eda1f533943bd9c6b",
	"S14c":        "a5f5a978a7daa49ee9cce03f58d4302be9c97342be3f9fd27f526594afb643b6",
	"S15":         "d204c794e90cbf9de7f5d255b25ef6ed7f4b53428390445d033f2c395193af59",
	"S16a20":      "aefabcb1f9980a51501a81b6f14b0c129215dd4b7f31c6270c58f8b11b90d1b8",
	"S16a34":      "a9d98f87df079c08eab2d36636547242a75261b2bf96b5b07fbbf0bab60aa50d",
	"S16b20":      "b28e74551ce7fd090641b1674cfcd6f98b72cf5a3c696af8008c90e0fca25fc6",
	"S16b34":      "0e2a8ae55de11fa62fa5ccd44f86bb613b3fdf924ab6fbe8680900cec23eb1fa",
	"S16c20":      "384c7afa92f0b9698e1a334d6e4170ce8662ceec4749a82a0dbe9d4cbed51120",
	"S16c34":      "c1e08d1b21fed21c26ecf37a2bc99b816b7f2e156aeaa1dc7dbb981f7a7365f1",
	"S16f":        "3137ce21004e79ecc03d4a757870d7dff85e7a63df2183ccad301e212e682e1f",
	"S16g":        "e756fa971f07396860ba0ad25492af79aa018034d04f9f3993d62ec3518f4b4b",
	"S16h":        "7dd806545f6dda776d92d0691028427b22e0f8620bcc8689d343aa957a009232",
	"S16d":        "e2a19e5a3f502e7ada1282659cbced31afb3ff37f630945157bea64859817795",
	"S16e":        "bd9dee0c692989ad291e0ecf717d5410766e81ad671ee718c79c54cdd3a3f821",
	"S17b":        "62ea42b05adce42307f3f8ed6d5a271d050b3fc54b727d15180471b8fab798f6",
	"S17":         "39b5431b540597e25ee62c3381169dcdd7cbf1371963b2f9f9dc4362b53c76f4",
	"S18":         "98fe6c9ae1789a35f6a8f0b3e95942044cf7bac7a5333679d11253605560f6aa",
	"S18b":        "405b9361ebebec221106cabc226d17da2844d35340cfbd4ad7a65473e91ede06",
	"S18c":        "b6439b0f025133bdf214cb896eca68f3baf315ca7fa8b76ca8d590e0105c7b86",
	"S18d":        "87c448db9a7ff846f6d29475580641a526163506dea50631228fec221f927629",
	"S18e":        "db171e2ae59093860b25e25c5dfc2ce40fd8c56a8945d7696f687f72b27652c2",
	"S19c":        "a95f23d0d967628328f2459421004b928948fc445bc993c7c1474f0993af3f0f",
	"S19a":        "150ef6495a80f79dcbf25b72a9332f2500991181d9cf240c0fdf7a0ab64261a6",
	"S19b":        "f0e4b773e100d65589f963db8ed9ef977b6991f2fb184086b3e11a144d6f5238",
	"he44-ps.aac": "9a40938beaeb1a033458670a1976943644bf0442088f3ba1057ccf9bb85b17f6",
}

// TestPSOutputGolden: the decoded PCM is the recorded one, bit for bit.
func TestPSOutputGolden(t *testing.T) {
	var golden map[string]string
	switch {
	case runtime.GOARCH == "arm64":
		golden = psGoldenArm64
	case runtime.GOARCH == "amd64" && cpu.X86.HasAVX2 && cpu.X86.HasFMA:
		golden = psGoldenAmd64
	default:
		t.Skipf("recorded on arm64 and on amd64 with AVX2+FMA, not on %s", runtime.GOARCH)
	}
	check := func(name, got string) {
		t.Helper()
		if want, ok := golden[name]; !ok || want != got {
			t.Errorf("%s: output SHA-256 %s, recorded %q", name, got, want)
		}
	}
	for _, spec := range psStreamSpecs() {
		b := psBuildStream(t, spec)
		cfg, frames := aacADTSFrames(t, psStreamFile(t, spec.name, b))
		l, r, _ := psDecodeFramesMode(t, cfg, frames, psStreamFrames, psTestMode{ffImage: spec.ffImage})
		check(spec.name, psOutputSHA(l, r))
	}
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", "he44-ps.aac"))
	l, r, _ := psDecodeFrames(t, cfg, fr, len(fr))
	check("he44-ps.aac", psOutputSHA(l, r))
}
