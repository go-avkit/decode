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

// psGolden are the SHA-256 of the decoded PCM of every written stream (S01 to S19, which
// exercise the layout switch, the freeze and resume, the carried matrix and the fade in of
// the first matrix), of the two streams below in which SBR drops out, and of he44-ps.aac,
// recorded from the decoder before the PS stages were restructured to run slot by slot.
// They pin the output bit for bit: a restructuring that keeps the arithmetic of every
// sample (the order of the additions, the fused multiply-adds) leaves them unchanged.
//
// There are two tables, and the line between them is the DCT pair kernel of go-fft, not
// the compiler's fusion: the pair of transforms of the QMF banks is a fused kernel on arm64
// and on amd64 with AVX2+FMA, and two single transforms elsewhere (386, riscv64, loong64,
// ppc64le, s390x, amd64 without AVX2). The first recording gave identical hashes on arm64
// and on amd64 (Go fuses multiply-adds on one and not on the other: the PCM is float32 of
// float64 arithmetic, so that does not reach it) and the CI lanes without the kernel all
// differed from them, in every entry, and agreed with one another entry for entry
// (psGoldenPortable, taken from the log of those lanes). A change of the arithmetic on
// purpose records them again and says why.
var psGoldenFused = map[string]string{
	"S01":             "52875a22e6af64ce6039f4910330297556ac9998fe43d3569e18b73149913ebc",
	"S02":             "cbeb7be081926a6174a1844d02bac7455e70c9ec8947840b35c4d1759d841029",
	"S03":             "52875a22e6af64ce6039f4910330297556ac9998fe43d3569e18b73149913ebc",
	"S04":             "562fb294ba9f75c60c9ac396d6c4a397dd6107a309310d309e37b579a85d3753",
	"S05":             "4ff39dd4b58558b1c8f617227a447a227b035d628395d6df09dd4d06962856ce",
	"S06":             "9b4bc697ec4e1bf3619e6bc874e91ef90c6638f8dac75636ca67fcc46f517f9c",
	"S06b":            "8852c0ffbf6318a42e1d28cedbc6178414ddb843f4d7c23c41778fadcf3a9aa9",
	"S07":             "e9d0c4603f8c9294b259a81c8d5d153bbaec9899f2ada60cf87d8bf79e05e6f0",
	"S08":             "fd4a84d5882c46b313436ce5dd18197133814e3dbc378fde17a590a462b7d9a1",
	"S09c":            "d4eb679f100662d02612315c66b260a43387edeb984f5b9c8d90703b9af4334d",
	"S09f":            "96bafc2731b6724a2caed630ad9a3a9c406ded439c7e2cc7940a1c977aac37fd",
	"S10":             "57b3540b2038ea00832587ddd0d675d6af361efc8f774d032fd098fc2e12f72b",
	"S11":             "d6774f090e42dcc60693c4a4c0aff9c75099fa8eb5001d8eae706a5755fd935d",
	"S12":             "416b6d73f586c5f8b95f2a516d6f0d8ecd4eb7b7b4ad934982e6a43256af8a80",
	"S13a":            "67bbc5d9995bcb28c87ddeba86d7c09bb818dd4751d96e130f5ec8e5d09650fa",
	"S13b":            "99c11665b06a7f09a42a5ecfb5d50750a02533b6c50a5b5527d6611b2157e9b3",
	"S14a":            "b739da82b6f9b2d99f57a3b20a4de44f69d37b8b62c1f307c25c0580a0754211",
	"S14b":            "5100fd25b48aabfb7b7ea11fdac6fa56affa8e51a22d476eda1f533943bd9c6b",
	"S14c":            "a5f5a978a7daa49ee9cce03f58d4302be9c97342be3f9fd27f526594afb643b6",
	"S15":             "d204c794e90cbf9de7f5d255b25ef6ed7f4b53428390445d033f2c395193af59",
	"S16a20":          "aefabcb1f9980a51501a81b6f14b0c129215dd4b7f31c6270c58f8b11b90d1b8",
	"S16a34":          "a9d98f87df079c08eab2d36636547242a75261b2bf96b5b07fbbf0bab60aa50d",
	"S16b20":          "b28e74551ce7fd090641b1674cfcd6f98b72cf5a3c696af8008c90e0fca25fc6",
	"S16b34":          "0e2a8ae55de11fa62fa5ccd44f86bb613b3fdf924ab6fbe8680900cec23eb1fa",
	"S16c20":          "384c7afa92f0b9698e1a334d6e4170ce8662ceec4749a82a0dbe9d4cbed51120",
	"S16c34":          "c1e08d1b21fed21c26ecf37a2bc99b816b7f2e156aeaa1dc7dbb981f7a7365f1",
	"S16f":            "3137ce21004e79ecc03d4a757870d7dff85e7a63df2183ccad301e212e682e1f",
	"S16g":            "e756fa971f07396860ba0ad25492af79aa018034d04f9f3993d62ec3518f4b4b",
	"S16h":            "7dd806545f6dda776d92d0691028427b22e0f8620bcc8689d343aa957a009232",
	"S16d":            "e2a19e5a3f502e7ada1282659cbced31afb3ff37f630945157bea64859817795",
	"S16e":            "bd9dee0c692989ad291e0ecf717d5410766e81ad671ee718c79c54cdd3a3f821",
	"S17b":            "62ea42b05adce42307f3f8ed6d5a271d050b3fc54b727d15180471b8fab798f6",
	"S17":             "39b5431b540597e25ee62c3381169dcdd7cbf1371963b2f9f9dc4362b53c76f4",
	"S18":             "98fe6c9ae1789a35f6a8f0b3e95942044cf7bac7a5333679d11253605560f6aa",
	"S18b":            "405b9361ebebec221106cabc226d17da2844d35340cfbd4ad7a65473e91ede06",
	"S18c":            "b6439b0f025133bdf214cb896eca68f3baf315ca7fa8b76ca8d590e0105c7b86",
	"S18d":            "87c448db9a7ff846f6d29475580641a526163506dea50631228fec221f927629",
	"S18e":            "db171e2ae59093860b25e25c5dfc2ce40fd8c56a8945d7696f687f72b27652c2",
	"S19c":            "a95f23d0d967628328f2459421004b928948fc445bc993c7c1474f0993af3f0f",
	"S19a":            "150ef6495a80f79dcbf25b72a9332f2500991181d9cf240c0fdf7a0ab64261a6",
	"S19b":            "f0e4b773e100d65589f963db8ed9ef977b6991f2fb184086b3e11a144d6f5238",
	"drop-S16a34-4-7": "97a2389ead2e97cb8e258cb93c179aa0a3158c83b7a837678dde7bd0624f0975",
	"drop-S09c-8-":    "c3a877f1a217c353ef2157479254cec689497c2e1f4be7a07716854ab10bef3a",
	"he44-ps.aac":     "9a40938beaeb1a033458670a1976943644bf0442088f3ba1057ccf9bb85b17f6",
}

var psGoldenPortable = map[string]string{
	"S01":             "f285d1b35f1b77453dba05b13ab044a9bb62b0781825b04dadaaaa0b62cfbc14",
	"S02":             "eb19cffec5b2161342c586afc06fd52f1821896b7be9c14b4395971b997dcac8",
	"S03":             "f285d1b35f1b77453dba05b13ab044a9bb62b0781825b04dadaaaa0b62cfbc14",
	"S04":             "09f909a9bc13bb241283956c1c2b9ca248d44a67000dbbceec55afcc469bb28e",
	"S05":             "b037e2bd1025b89c1ea269dccfc21f0f7074f9d3c48fe3ffb84720ef13953e5a",
	"S06":             "179850868c4c3b101589dd8df93599abedaca2d6c5ff0fb100ada9dc9c00ced8",
	"S06b":            "7547c715a2e093dc87f7c9074f07aad6259e1bb846f35a721924a71f94f4f8ae",
	"S07":             "3d0b74627bb961d722319010eae7dbd4a351cd3ec33c6c7492d10583df987f69",
	"S08":             "aee798947e96a29be2690496d452384c1cb0a42c867e6039429e6871fe9e0733",
	"S09c":            "13a25b48321aa95d43c1007c10cf937ba79f1aa0c95791db1c90524501bafe49",
	"S09f":            "0411e06a9be65e26fa77d350476958934c9ac980b72839397dc95f8f550e3c24",
	"S10":             "86b93d6fd0480b876748f559d72606c12ee54aa722e6ce1446c13c7cea27f0b5",
	"S11":             "67f124a6c1368eaceb830b05e46c7e4ab16d96dc64a89d610077b3eea92fef40",
	"S12":             "2841f0711736f636594f591a484bdc225da34862b9304ceea1aadf9236c307fb",
	"S13a":            "34c96c09b907f58cf1187cf343a8bff35782066360a0d89d5cd5ef16aeab1dbe",
	"S13b":            "d146d832a390538ef06ca8debb898028a621cbebbf45f6707af9fd258a93fbf6",
	"S14a":            "b2941b593ec5b1e31c5e0a465546eff140761423bd6abb7e1f11190422cff2f2",
	"S14b":            "57e9bdcbca0cecc58d4c3322492161ea90a1223509d9ac976c0bdbda05ab4860",
	"S14c":            "95fbc1355dac10706589cc411c23022857f1aedc8d07a4b041792004db5bc0f9",
	"S15":             "5c6d935682501d6d39e9da690173d3937aa14144d7f8b18c9c849744b6cee560",
	"S16a20":          "8f0f542fd47cfc99a0e8edcb468a49a632add055c7ffb14360cf4f1399b9f6d8",
	"S16a34":          "33dba4370db25c8d4da28d7fb3a4647f3122cd43739606cee7be2d3de9c06ddd",
	"S16b20":          "8fe0b395fbfc14a0117d170c5da7bc4b1e1d3d46726f88bd16c73bdca0828025",
	"S16b34":          "e8333791438e46d35803e3696067b10c7f049acd868047dc75ab06e8be13d6ef",
	"S16c20":          "6fbf93241bfe5663b59e0e13940c8175443a337afc27f0482af87f7191f3d1d7",
	"S16c34":          "e5fdf2d50939d108eac633d637c1a7a474e58109de76b4641ce8d6b77bfe2be7",
	"S16f":            "ae250efe9a3b7dff051f4610076f82d71d1228ccfec12d53629986197974f688",
	"S16g":            "51b44165a02e15d59386ee612d6d3403d92f8f146e8aa906a2c367873d729cf0",
	"S16h":            "45bc6885addebed6991611c2cd661c261de2c4c208f6fd378f224ee7fada45c2",
	"S16d":            "c6d2f423f7b1666a98db51f79946fecb9a077f74e1627299fbf8f9ef00601551",
	"S16e":            "79da74acc2402993308a0b974694096669373b59e929d4304c5131b8a0515009",
	"S17b":            "6b8bb6c6a34b69c1b01b7cb5962b82ffdefb4cc7ec6f16a22972b2043338596b",
	"S17":             "95ba471bffd65a2374bbb1ad9d50c0fd6490fb40574e09bcae2c6508ddb6cb70",
	"S18":             "83d8b50288f5c46abfefa617a248863d985fe93441306e506d451419aa85f1eb",
	"S18b":            "2d6ee480cf99f8bdd2778db1ef7c98626230b20513ba1228ac494e087ba10ab8",
	"S18c":            "ea29da36922adde38ae72461516cd443d5ba18dd59e2bcdc60c404f16d5f23b5",
	"S18d":            "5332bc61000a8fcc926b7594da58bfeea821e4a26ce1e22ad0f0815b451da850",
	"S18e":            "1801dfcd336e700597ef4c5d4b8a5d6cb8ae04255bdf1f8f2e6fc26a308f29d4",
	"S19c":            "45af49a5fe4ec8ea37dd1a48d43e2c689e9298ea98694aef5c099f6e2eea472a",
	"S19a":            "10277cfb585de4fbef0cdd2e63599f153d9d197026cdad1e0d864486c8b05b81",
	"S19b":            "38cba22ecde6aaaa831c21fdbd47098f076320903f5b4c33af86b3527664a611",
	"drop-S16a34-4-7": "e7172e9ec6d6b54c3eb388629ebc740cccfb79e9dcdcab6aee484363c1c4a8a2",
	"drop-S09c-8-":    "22d016185f008dd8c77bd3fa3e27b0815c16de779e3e9cb7b70cf3898e10bbae",
	"he44-ps.aac":     "d86095cb1b5a0b1384c9668e9c2e8bd789fe51ad21c54176d6e7d0ea39a56331",
}

// psPairFused reports whether go-fft runs the DCT pair as one fused kernel here.
func psPairFused() bool {
	switch runtime.GOARCH {
	case "arm64":
		return true
	case "amd64":
		return cpu.X86.HasAVX2 && cpu.X86.HasFMA
	}
	return false
}

// psDropoutSpecs are two streams in which SBR drops out, the frames of S16a34 (34 bands,
// IPD/OPD) and of S09c with no SBR fill element at all (withSBR false: PS is suspended
// and both channels are the mono signal until the SBR frames come back): a hole in the
// middle of the stream that ends with SBR resuming, and a stream that never resumes.
func psDropoutSpecs() []psStreamSpec {
	var out []psStreamSpec
	for _, d := range []struct {
		from, name string
		drop       func(i int) bool
	}{
		{"S16a34", "drop-S16a34-4-7", func(i int) bool { return i >= 4 && i < 7 }},
		{"S09c", "drop-S09c-8-", func(i int) bool { return i >= 8 }},
	} {
		for _, s := range psStreamSpecs() {
			if s.name != d.from {
				continue
			}
			s.name = d.name
			s.frames = append([]psStreamFrame(nil), s.frames...)
			for i := range s.frames {
				if d.drop(i) {
					s.frames[i].noSBR = true
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// TestPSOutputGolden: the decoded PCM is the recorded one, bit for bit.
func TestPSOutputGolden(t *testing.T) {
	golden := psGoldenPortable
	if psPairFused() {
		golden = psGoldenFused
	}
	check := func(name, got string) {
		t.Helper()
		if want, ok := golden[name]; !ok || want != got {
			t.Errorf("%s: output SHA-256 %s, recorded %q", name, got, want)
		}
	}
	for _, spec := range append(psStreamSpecs(), psDropoutSpecs()...) {
		b := psBuildStream(t, spec)
		cfg, frames := aacADTSFrames(t, psStreamFile(t, spec.name, b))
		l, r, _ := psDecodeFramesMode(t, cfg, frames, psStreamFrames, psTestMode{ffImage: spec.ffImage})
		check(spec.name, psOutputSHA(l, r))
	}
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", "he44-ps.aac"))
	l, r, _ := psDecodeFrames(t, cfg, fr, len(fr))
	check("he44-ps.aac", psOutputSHA(l, r))
}
