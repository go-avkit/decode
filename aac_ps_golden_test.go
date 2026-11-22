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
// There are three tables, one per route of the float32 QMF banks (go-avkit/decode#77):
// arm64 (the NEON kernels of go-fft's DCT pair and of floats.MulAddRows32), amd64 with
// AVX2+FMA (psGoldenAMD64) and everything else (psGoldenPortable: 386, riscv64, loong64,
// ppc64le, s390x, amd64 without AVX2, where the pair is two single transforms and the row
// sums are Go code). The banks' PCM is float32 arithmetic, so the order of the fused steps
// of each kernel reaches it and the routes no longer agree bit for bit; every route meets
// the bounds of aac_sbr_qmf_bound_test.go. A change of the arithmetic on purpose records
// them again and says why.
var psGoldenARM64 = map[string]string{
	"S01":             "9181943b30190e4e14657908eb06ee7f431b9096341fa3d42614d5a1ad2065ed",
	"S02":             "4a0bfcb4e45d4b2e65ee87c586ecd55a4ac5436a2710488be42c231b6a63b83a",
	"S03":             "92a9010bdd057949f9d177699eed4108949ce1df0be4c4ac845e665ea957fb1d",
	"S04":             "169447863441a550d7567342a2eb36303beea69c3beca1af58e949d5897d961e",
	"S05":             "62c3c6688a7f8e1a0f0659abbc925517d8e16245249d80a7a9b9b12e7da92d13",
	"S06":             "1b2106845de5555553d70c89f54907c2224fcf963132ae55a6717ebac6fc4e09",
	"S06b":            "018828c9a3acc51bcca27685a72512fa13de4315ed5512dd14e51f3349072256",
	"S07":             "3dd0d38e650ee9df172fa58e70f078e5a812f7e51ae09e953d35ad031a88ee5b",
	"S08":             "2625aab5205f73632e4a872b499f226d666e436396a9d49141302deb1d144203",
	"S09c":            "b21549b419f4383d9d69ee568e78934b208126493c5e43ac11bd7789db4819fb",
	"S09f":            "d4bc955cb2ca024329b72578c145aceed55371f955e1b1d926afec5a567b5034",
	"S10":             "69c061354016f53cf1e005f93f76ea6a42744df8fc8c1b9d1dbcabbc7b1aa7ae",
	"S11":             "4baa57124ccf3f493ebe7319b760447e252b7f20b01572ed11a348559c4e7c22",
	"S12":             "c72e20c58e1757601415e0081e371843d70f9f122e988203c554b64dd24a13c6",
	"S13a":            "813f30f1efbe9c5f79da4634af3b4b8821902c9473c184f57271c02e1fe5403f",
	"S13b":            "0d7f7ef58252628b26355bcf572c9ed416ecac00549d3e9056bb6d2fc0fcd205",
	"S14a":            "62c0bf5e440884259e78bb97c390de51bcf4d096f911b567457837134fff993c",
	"S14b":            "8683cbf0d975f18878ac562475b721279389db9b5e5a58ad046a8c30d196d1d2",
	"S14c":            "cbb846fe908c2c9f426b79d228a45f79a8e9c93a4a8c6384807434fe8a8e26ef",
	"S15":             "ff660285e3bd26afed0104422b967a992227039a09fad71c9087c7c60012ba59",
	"S16a20":          "23d9b517ffcc1e006c896a86d33f69508418afbe037b3feb9d4c8e259f5356aa",
	"S16a34":          "ab3c97e886e07dfa36fa99f84d6f6f5078298102926af5ed353625f7f5021edf",
	"S16b20":          "792b6dcaf6d341acf082a12d9c5d0b3f30b4ee3bc5d4a6022f2d5ab4aca52924",
	"S16b34":          "ee6ec2d4de09db3515939f29b01294032f3fc01bbc5ae73f207299d279057cf8",
	"S16c20":          "275f02ccd96d3f0a856387f54f1fc313845377e6503f11efa4b75036d217adb0",
	"S16c34":          "b43038192b98a07c383a3337d06badd1c9e42fdac6400477a3c90438ea4c3f8e",
	"S16f":            "a7871d568e1a775d5d4560c87467811993c4a52b6a361a342690d684eb578d6f",
	"S16g":            "aaf6caceaf9f0d8090f03f82df9c5fdf7e6969e876c53e013d947674b64dda08",
	"S16h":            "40c85eca933e366aa42d43a0a3d904b41ddd78414785326c09935ae585ecb81f",
	"S16d":            "db7c7f0a1dde61d2589a82198cf1918488af7412467276a5a2d40e2d637a9629",
	"S16e":            "335a63695c20f6706eb86721d5400a6b4dff62a9b50301204b2d141523fac8cf",
	"S17b":            "6111946f01a7b87333740d6879f6c760990743b724009d2d02e06f66d07552f6",
	"S17":             "ef73b8ae256b42090537b61b6e3d99f06482cbef805e625f55e448da4d85d304",
	"S18":             "ec265c92ce67923c0604750cb988a88fb11f69e51840a3f023a0d066c6b0a394",
	"S18b":            "996773f5056fb88d0c8cccc78947494f9069d18c5c72a9e49fc3f40fa3cfd531",
	"S18c":            "cc650fc528160a38a50698b27564daf4a3094001010d91e03c840ae2a6eecda2",
	"S18d":            "2aacbe5a2a19b452c8a0ccec4e65ab9922a6958ceb7e3a6abe3357aa89ba4410",
	"S18e":            "7da8d47e8b201a5114ab99667f6f0b9519367796590e490fdf24d1dd20ff3a3c",
	"S19c":            "79d232895da69c9e3ecbee1337055722516c1c6c481f01640f97e71cb5e46428",
	"S19a":            "2dbf536967e5afc1df7514965520b824577029bcf1aff1d5b64926c5353bf3ef",
	"S19b":            "13bd595048e4629da477ee0da4bf11a4b22988966bb9d2bbee4d775c307aa887",
	"drop-S16a34-4-7": "6ce36c537d289c1ace3a5e485162ef54937845b18098467e6c2aabf9f93159eb",
	"drop-S09c-8-":    "2940db567f83fbee4326b6b1344224bf3ba5198da00bdda22ead3137d11e818c",
	"he44-ps.aac":     "daaf22be52399f16033476ec0d3891c833f2ec710cd717cd693bcc871ae3a96c",
}

var psGoldenPortable = map[string]string{
	"S01":             "9fcf2613e39bf976aa2b265cbed47cf1c4510ac2d8b9d0e7bb6028f60d2fc812",
	"S02":             "be50cb3c33cbd9f8e14f4c7b6001105d15a3bc63b9312d03efa497afdf989b9b",
	"S03":             "be0d14b861b111200daba28aebf6e944fde434f40fe91563d88af61e13b17f1d",
	"S04":             "e1f99e21f5b9250cea9ed2d2c0ed1cb619fa8594fab9c9fff659f924dedf0242",
	"S05":             "baba31e61d551bbcb997ee26f2d905ef40dddc09c7cfc60a1f0f9f7a6e19f407",
	"S06":             "ef12d7c058741cd038b07250559a332b4785150d8f88c0e08bd33595320088bd",
	"S06b":            "8451fee80b1d84ea351454ca270c30c3e61eae08c7b0884c2905e04f6ca09fe7",
	"S07":             "1df3870aa58cc2cbbc6ae0f71f91428e53938f6b5dddf04645cd802b45ce9879",
	"S08":             "e97860a8afa30ee02fd85259cf84020b65aedf9c94952e3da4cb1f0054b9c99e",
	"S09c":            "077004485c7937f39a2328293a21054b904d6f376c6aba67e7d0bd42044bcfe3",
	"S09f":            "b158721f663b8c26c36a6d534d5543af78d545a706878058516005e075b8c1c1",
	"S10":             "4a890d2398232ba62d7e8f9761bb2917dcafc8e65747e4ee9d5ff0e18e75d765",
	"S11":             "2b924081ec7e85dc11ef00dceb6b5721d5292b925f8b6174959aefc0c689b453",
	"S12":             "2cf0e9b41577b4235d3acb7edc28924a447c419851d9e78d1135e42e8406d601",
	"S13a":            "dba368a9a91c9746e25860357eeeae7ba4011ac0bd7f0b89e147b13e5f42f2f0",
	"S13b":            "36a4ee4b2fd74b7510e640e9127dddea5d77941643d1f603df48cbe48190a227",
	"S14a":            "0f2786f63f6a29d105e81f5430338d63263d84c37a9e25aeda8a4c6561c0fc79",
	"S14b":            "a81a7a40d87f1d03be14fbbd4aedd6a0247ad800f1af0ce72989a84111e6dbc4",
	"S14c":            "39ba9e76c8c9d6194f278774fc50f45a53b67a7c646a2563a43a64dd8e9cc629",
	"S15":             "719cafd3a4481c02a65028c6ef101bba59b237fbefc3bf668ee7b4d7c1a6920a",
	"S16a20":          "0da1450847e76517a0c3edc02f21d657d34b04a71b304402a8af1db9047fc7e2",
	"S16a34":          "0dea656ab719038b237329b30c2dd063a025eef05341c94f4e4ee9c4a9ed63d4",
	"S16b20":          "9823dcd76789275c42e2c998291a8ad29d2ff06f88d6acd9fd218c00f72d2fb8",
	"S16b34":          "698e8966fb7d978bc00ac15ac304c469b9aab9b0de807798d7a1354b72d8fb35",
	"S16c20":          "78e52b1a672baaa0135d624d5054e5b539206a1e78aa30e421d7789317413681",
	"S16c34":          "6a8762fc9a26fdc4beb8b80d7c85b6c07b28981b381b45bd24bc92bc94b7f4bc",
	"S16f":            "7cce67938ca3f80456bd0d62a13196339e8a49b8bc0e99f4f5d04661a1573133",
	"S16g":            "bea3d2cf4090f9a8104e7567663bad3583f70ad5a9117e4c975cef2d9a49db42",
	"S16h":            "f41b0d7611ef12b0c3053bc522df345c99f012326128f04d0356f4dc541d7832",
	"S16d":            "42c68d5661159e2416bbe6bb34a9f8e7e8a658141ae9cb7bf941ca22056cf02a",
	"S16e":            "289eccde79ad8ecb99316fb0b36a6a18dba3c35a91f8ca94d0e9fbbf07d00285",
	"S17b":            "64682346df734cd27428ee1cd4b76890d1f94ff1a9a98f52484375c17f867be3",
	"S17":             "83df1636d0468bf486a0b6de28f2029e737f5a236e443a3d2e795951b20ee612",
	"S18":             "2e6a56178cf951dfde530b55d4ad14aaf8a770255999c72ad3edcf706a957de7",
	"S18b":            "334bfb569987b5bd07176dc16bedce4a058c61960fefc689e6413595ac972399",
	"S18c":            "f2e7533ecc29fe5965c01e30f24254233c44959de30d1915c3ba5ec135847151",
	"S18d":            "a0a2702665be806972d7efc7a2cafa70436678b762ee7771153907028f8c871c",
	"S18e":            "44311e870775f3a1535ce8ff9a3fbd1e2ac65cd2136038c9434d7ab7673736cf",
	"S19c":            "1890321011adb0920abd76b0451e893690723c2d29830c42da6f35d269c7b0e0",
	"S19a":            "33b00bb339001603f67cc2e1d44cc24653517cbc3503afa5162591346903bb5e",
	"S19b":            "9b9610f874eacfd3062ff3fb65588c157bf976fe7702e6a9fb6f41a296c2e5e0",
	"drop-S16a34-4-7": "6453b9554b95c78d0c5cdca53c09ae1b290bc423d4d40be96b0b4c480105edb0",
	"drop-S09c-8-":    "57f268426772091923684037659fe50e48d22e2c8a016e06d16ec51283516a39",
	"he44-ps.aac":     "7df5991c8f130e45bf89db70fa1f40b3d0c1dff54590951cd15f6d6c73b69f66",
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
	switch {
	case runtime.GOARCH == "arm64":
		golden = psGoldenARM64
	case psPairFused():
		golden = psGoldenAMD64
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
