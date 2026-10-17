// Package voiceassets discovers and provisions the standard local voice profile.
// Downloads happen only during an explicit setup request.
package voiceassets

// Profile identifies immutable assets compatible with sherpa-onnx 1.13.8,
// ONNX Runtime 1.28.2 and GinkGo's audited keyword-clock adapter.
const Profile = "sherpa-1.13.8-parakeet-v2-hey-ginkgo-v1"

const parakeetURL = "https://huggingface.co/csukuangfj/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2/resolve/86891485dd8ad7cb28cb1aade45c3e23d0197c30/"
const kwsDirectory = "sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20"
const kwsURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/kws-models/" + kwsDirectory + ".tar.bz2"

var kwsArchive = Asset{URL: kwsURL, Size: 32885699, SHA256: "68447f4fbc67e70eee3a93961f36e81e98f47aef73ce7e7ca00885c6cd3616a6"}

// Asset binds a configuration key and relative installation path to exact bytes.
// Member names a regular file in the pinned KWS archive; Content is bundled text.
type Asset struct {
	Key, Path, URL, SHA256, Member, Content string
	Size                                    int64
}

// Assets returns the frozen FP32 profile, including the encoder weight sidecar.
// Parakeet hashes match the pinned publisher LFS objects; KWS hashes match the
// native adapter's audited profile. Silero is the 16 kHz/window-512 export.
func Assets() []Asset {
	return []Asset{
		{Key: "voice.sherpa.parakeet.encoder", Path: "parakeet-v2-fp32/encoder.onnx", URL: parakeetURL + "encoder.onnx", Size: 41766257, SHA256: "7ce8d2b3f45fcd3b553d3b7a188436db7748c271081cc004f28bf76f3df01893"},
		{Path: "parakeet-v2-fp32/encoder.weights", URL: parakeetURL + "encoder.weights", Size: 2435420160, SHA256: "90cd4bb6c9b60496d49be9bec3a844f4e9bf22c62a45ea93f391cc00f9a47cfe"},
		{Key: "voice.sherpa.parakeet.decoder", Path: "parakeet-v2-fp32/decoder.onnx", URL: parakeetURL + "decoder.onnx", Size: 28883663, SHA256: "0140d12782ccf550a9709f03a52c2782b3c54d045f47ce14af39c713ab42de7f"},
		{Key: "voice.sherpa.parakeet.joiner", Path: "parakeet-v2-fp32/joiner.onnx", URL: parakeetURL + "joiner.onnx", Size: 6907576, SHA256: "a9e57e488cd1016cbefd51f60712896af6590e3f61ff466a540e085bbd6af59e"},
		{Key: "voice.sherpa.parakeet.tokens", Path: "parakeet-v2-fp32/tokens.txt", URL: parakeetURL + "tokens.txt", Size: 9384, SHA256: "ec182b70dd42113aff6c5372c75cac58c952443eb22322f57bbd7f53977d497d"},
		{Key: "voice.sherpa.kws.encoder", Path: kwsDirectory + "/encoder-epoch-13-avg-2-chunk-16-left-64.onnx", Member: kwsDirectory + "/encoder-epoch-13-avg-2-chunk-16-left-64.onnx", Size: 11976272, SHA256: "540ff509ed89bd22afe04bf7049a54bb1c95c6d8a18742ea9691910cdb5f859e"},
		{Key: "voice.sherpa.kws.decoder", Path: kwsDirectory + "/decoder-epoch-13-avg-2-chunk-16-left-64.onnx", Member: kwsDirectory + "/decoder-epoch-13-avg-2-chunk-16-left-64.onnx", Size: 759829, SHA256: "63a22dd60f40fff082ac3e09afa507f6787da36df76ded2fbe145fa233e22c21"},
		{Key: "voice.sherpa.kws.joiner", Path: kwsDirectory + "/joiner-epoch-13-avg-2-chunk-16-left-64.onnx", Member: kwsDirectory + "/joiner-epoch-13-avg-2-chunk-16-left-64.onnx", Size: 338154, SHA256: "76f7a24ed0c08633af14b2ee377f747af880d3b65eeba2cd3f31f3380fb73e8d"},
		{Key: "voice.sherpa.kws.tokens", Path: kwsDirectory + "/tokens.txt", Member: kwsDirectory + "/tokens.txt", Size: 1928, SHA256: "2d3f32311f9b692b964da3c90e830258d3e78e013cb0c992dbfb15cd5a1a71b0"},
		{Key: "voice.sherpa.silero_model", Path: "silero/silero_vad.onnx", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx", Size: 643854, SHA256: "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6"},
		{Key: "voice.sherpa.keywords_file", Path: "kws-hey-ginkgo.txt", Content: "HH EY1 G IH1 NG K OW1 @GINKGO\n", Size: 30, SHA256: "9144d7ff0c478303fd9d3ca99e01d19d438ed011154247847fe08118fc9b84f7"},
	}
}
