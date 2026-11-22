// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

// psGoldenAMD64 are the hashes of psGoldenARM64's streams on amd64 with AVX2 and FMA. The
// float32 QMF banks (go-avkit/decode#77) make the PCM depend on the order of the fused
// steps of the DCT pair kernel and of floats.MulAddRows32, and the AVX2 and NEON kernels
// order them differently, so the two machines no longer agree bit for bit; the bounds of
// aac_sbr_qmf_bound_test.go hold on both.
var psGoldenAMD64 = map[string]string{
	"S01":             "ed90d5cc27eb87e4b7386b54715d8df59eac2f3e04d099da4dbe34a795f90f65",
	"S02":             "f9dd70f8404cbf73ce1d490363b1a71a78cadef1898182b7dc2336b57bb8501c",
	"S03":             "610e43d6ec5c8ff06aaa847e558cf913358512804f5cffb4f997708005a0774c",
	"S04":             "43b418fffdd443953632d9f6dd738b92f3a66f1c9706274590ccb31dc0ab6595",
	"S05":             "00b10e65f630afb5435b4a4c3ba738fb014c1a66b6d27806d818ecd877049a97",
	"S06":             "d5cffa753fd2ab62a8fb29e7f54b4e418e0a53d07e99282d6757d041d9425989",
	"S06b":            "308d05fb1e53b26ef7540cf035ade68e501a8473d52d4ab87122982484bc1fcc",
	"S07":             "7f283fb087a88b6596886c4a27a36526f76747df8e56bac8e0c5af417b2feb12",
	"S08":             "347d74f5fb72aa25d4bdfa3af4a78ab6060e0c9f9ab8c707925035c26586c542",
	"S09c":            "6a503e1ccee3de8256693b4547befb0d44b8effd5b06fec0031314769347412d",
	"S09f":            "83aeca10fdb64a94668ac5e1e464123a3617deb54f2336194991f2ef7d8bfa9e",
	"S10":             "d08dfdd318976bfd31b1709ea192a4a7efcb94a91746a1c61079330cb83799ec",
	"S11":             "29f9b90de0de1002eee3075877bc9edea98d99acac7cd95c82f82d062a11f446",
	"S12":             "ca8e0547a96a4e9491bc26d184a3898f4d022b2f7103bfa32e1350fbf0066301",
	"S13a":            "ec81b9ba44b6eb9e35eab0e5691d8468c0abcdc1ab5a45087ac7a96b45263ff0",
	"S13b":            "7fb170aec89e4fc388cb5a44269ec286ff9940d21b409ae74cafdeaaeacabb95",
	"S14a":            "b56f5035f726b0637e9affdea026d99e790f5ef595a553e55eef593a94399cfa",
	"S14b":            "65a3f0564a708955c4882dcdc67b80b743c063d2bcbd0904de3966a3ba37e436",
	"S14c":            "e6dd48cf00408813e81ff3f75a89042686abeee5ca15260a472765e4be4f5d61",
	"S15":             "8eef2413f9668d8cf8e37a4d9394ed82fede1ab5e6c1de522cf622680518ee38",
	"S16a20":          "ae99e024e74e12828acbf6c39b3d26f6ae983362b24721718675de464c8c07f1",
	"S16a34":          "308e0d82ac707d2c19069ed04a4f77b499a595d692cf8eae6349cb9627653b49",
	"S16b20":          "ea19da31a528f401e71934971fd499c022893479d65aee5085f7a18e6c0ae009",
	"S16b34":          "407bb62b5ea36b77a64bc169d800dacb0cec80dd20f99be110f9733e2bf08aef",
	"S16c20":          "4df4e632dcfb26d56db31e03bdf0762b0bee6cdf116c9bbe96ab8c5f0ec85e2a",
	"S16c34":          "aae62684e76385e5e513d3ef1c3d0f7aee94481028fe4dd1215840fcafcaddaf",
	"S16f":            "0f2f59a298bb1293b39527e94cce7bdb016421f3747b76172797a310a4666f01",
	"S16g":            "92ab5675f776af70d85318fc4386f8fc52a495eeaa853f9de4fe69a8b78f5499",
	"S16h":            "ba40fefdaeaea0ee96b3a8174d3e20a65afa4ff7a28c66c04b288687da527305",
	"S16d":            "a14d8f4a889f1a327b2e7bfe91d4aef7a224de62e7e222e50920f174d2deabe2",
	"S16e":            "3063dcace6c0e64be2a23cae79e9be1356989703593ae98b8c709cbccc3f6104",
	"S17b":            "e8bbebb3f76f744224f7b55b84cecf255278cee8f35622e93891f59e759459ef",
	"S17":             "a414d759dc8e5e9b462ba812bbfa9c8d2ecc0143fb16c1a423c64a3c2f4b8027",
	"S18":             "a598d00286196f677d7d856cd12a14b4a3312ece72b5a5e4645abc92d40c727d",
	"S18b":            "1f46e309e6ff235254f49fa4b13f556e84e1475c4adfdd1561408b565ddf714e",
	"S18c":            "22cf9490adedf33bcddffca9bfda71a012f1b2c0f75e3fdb17c3ff7453febb45",
	"S18d":            "b84648ef48edb478908d7be45701e5b8a807209d465738b2485f9c2c318c06b4",
	"S18e":            "0193061b8fe04b21417c761a7c2831b063da55a626c6d0e9d3747318b519cd38",
	"S19c":            "f6673247307ab180c5422ac4e50ebd80b914948af95b9cf3e3f2d002abfdbaf6",
	"S19a":            "39cc1ff60039378a54821526c0c7d07c35fcf538753e72e5adc61bc20ce928c0",
	"S19b":            "e8ae32f5d2127d0e7ac97ecdc99504a63f90455445d37fed3e12c0226260affe",
	"drop-S16a34-4-7": "1a785091409358a8fdcc4860387ecafb1c045e313d20265f0a3eec4be1b9b345",
	"drop-S09c-8-":    "87121c7f1809815d5bfdcc9375bb0869629d142df0347217384cd4bf5a05314c",
	"he44-ps.aac":     "e8143fdbb2df99cead32adef1a15eddc8c299dc473e6ef3ad66e1ee9c154275c",
}
