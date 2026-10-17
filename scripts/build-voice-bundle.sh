#!/usr/bin/env bash
# Local-only Linux packaging. Never downloads, installs, starts capture or edits config.
set -euo pipefail
export LC_ALL=C

die() { echo "build-voice-bundle: $*" >&2; exit 1; }
usage() {
    cat <<'EOF'
Usage: scripts/build-voice-bundle.sh --output DIR --sherpa-source DIR --sherpa-build DIR
       [--sherpa-prefix DIR] [--ort-prefix DIR] [--helper-only] [--with-lsp]
Requires existing pinned Sherpa source/build and CPU ONNX Runtime 1.28.2.
Output must not exist. No downloads, model copying, installation or capture.
Installed prefixes can override C API/ORT headers and libraries; the pinned
source/build is still required for the ginkgo-kws C++ shim's static dependencies.
EOF
}

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
output= source_dir=${SHERPA_SOURCE:-} native_build=${SHERPA_BUILD:-}
sherpa_prefix=${SHERPA_PREFIX:-} ort_prefix=${ORT_PREFIX:-}
helper_only=false with_lsp=false
while (($#)); do
    case "$1" in
        --help|-h) usage; exit 0 ;;
        --output|--sherpa-source|--sherpa-build|--sherpa-prefix|--ort-prefix)
            (($# >= 2)) && [[ -n $2 && $2 != --* ]] || die "missing value for $1"
            case "$1" in
                --output) output=$2 ;;
                --sherpa-source) source_dir=$2 ;;
                --sherpa-build) native_build=$2 ;;
                --sherpa-prefix) sherpa_prefix=$2 ;;
                --ort-prefix) ort_prefix=$2 ;;
            esac
            shift 2 ;;
        --helper-only) helper_only=true; shift ;;
        --with-lsp) with_lsp=true; shift ;;
        *) die "unknown argument: $1" ;;
    esac
done
[[ -n $output && -n $source_dir && -n $native_build ]] || die "--output, --sherpa-source and --sherpa-build are required (see --help)"
for tool in go cmake git readelf ldd sha256sum realpath timeout; do
    command -v "$tool" >/dev/null || die "required tool missing: $tool"
done
[[ $(uname -s) == Linux ]] || die "only Linux ELF bundles are supported"
[[ ! -e $output && ! -L $output ]] || die "output already exists; choose a fresh directory: $output"
output=$(realpath -m -- "$output")
[[ ! -e $output && ! -L $output ]] || die "output already exists; choose a fresh directory: $output"
case "$output/" in "$root/"*) die "bundle output must be outside the source tree" ;; esac
[[ -d $source_dir && -d $native_build ]] || die "native source/build directories are missing"
source_dir=$(realpath -- "$source_dir")
native_build=$(realpath -- "$native_build")
revision=11afbd009a7f8c08f4bcf2fc1b265d0df4670fbf
[[ $(git -C "$source_dir" rev-parse HEAD) == "$revision" ]] || die "Sherpa source is not pinned v1.13.8 ($revision)"
[[ -z $(git -C "$source_dir" status --porcelain) ]] || die "Sherpa source must be unmodified"
bridge=$root/internal/sherpavoice/nativebridge
[[ -f $bridge/CMakeLists.txt && -f $bridge/ginkgo_kws.h ]] || die "nativebridge is missing; integrate the native adapter commit first"
[[ -n $sherpa_prefix ]] || sherpa_prefix=$native_build
[[ -n $ort_prefix ]] || ort_prefix=$native_build/_deps/onnxruntime-src
sherpa_prefix=$(realpath -e -- "$sherpa_prefix")
ort_prefix=$(realpath -e -- "$ort_prefix")
sherpa_include=$source_dir
[[ ! -f $sherpa_prefix/include/sherpa-onnx/c-api/c-api.h ]] || sherpa_include=$sherpa_prefix/include
for asset in "$sherpa_include/sherpa-onnx/c-api/c-api.h" \
    "$sherpa_prefix/lib/libsherpa-onnx-c-api.so" "$ort_prefix/include/onnxruntime_c_api.h" \
    "$ort_prefix/lib/libonnxruntime.so" "$ort_prefix/VERSION_NUMBER" \
    "$source_dir/LICENSE" "$ort_prefix/LICENSE" "$ort_prefix/ThirdPartyNotices.txt"; do
    [[ -f $asset && -r $asset ]] || die "required native asset missing: $asset"
done
[[ $(cat "$ort_prefix/VERSION_NUMBER") == 1.28.2 ]] || die "expected CPU ONNX Runtime 1.28.2"

# Only after validation do we create output-owned staging directories.
mkdir -p -- "$(dirname -- "$output")"
stage=$(mktemp -d "${output}.tmp.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
mkdir -p "$stage/bin" "$stage/lib" "$stage/licenses" "$stage/checks"
cmake -S "$bridge" -B "$stage/.native-build" \
    -DSHERPA_SOURCE="$source_dir" -DSHERPA_BUILD="$native_build" \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_BUILD_WITH_INSTALL_RPATH=ON \
    '-DCMAKE_INSTALL_RPATH=$ORIGIN'
cmake --build "$stage/.native-build" --target ginkgo-kws --parallel "${VOICE_BUILD_JOBS:-2}"
[[ -f $stage/.native-build/lib/libginkgo-kws.so ]] || die "nativebridge did not produce lib/libginkgo-kws.so"
cp -L -- "$stage/.native-build/lib/libginkgo-kws.so" "$stage/lib/"
cp -L -- "$sherpa_prefix/lib/libsherpa-onnx-c-api.so" "$ort_prefix/lib/libonnxruntime.so" "$stage/lib/"

# Offline module/toolchain resolution: missing cached dependencies fail, never fetch.
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
cd -- "$root"
# Quote whole arguments for Go's cgo flag parser, including paths with spaces.
source "$root/scripts/build-voice-bundle-flags.sh"
voice_cgo_flags "$sherpa_include" "$ort_prefix/include" "$stage/lib"
CGO_ENABLED=1 go build -trimpath -tags sherpa_onnx -o "$stage/bin/ginkgo-voice" ./cmd/ginkgo-voice
if ! "$helper_only"; then
    CGO_ENABLED=0 go build -trimpath -o "$stage/bin/ginkgo-cli" ./cmd/ginkgo-cli
    ln -s ginkgo-cli "$stage/bin/ginkgod"
    if "$with_lsp"; then
        CGO_ENABLED=0 go build -trimpath -o "$stage/bin/ginkgo-lsp" ./cmd/ginkgo-lsp
    fi
fi

# Require real native linkage, not a successful --help from a stub helper.
readelf -d "$stage/bin/ginkgo-voice" > "$stage/checks/voice-elf.txt"
for library in libginkgo-kws.so libsherpa-onnx-c-api.so libonnxruntime.so; do
    grep -F "[$library]" "$stage/checks/voice-elf.txt" >/dev/null || die "helper is not linked to $library"
done
for elf in "$stage/bin/ginkgo-voice" "$stage"/lib/*.so; do
    result=$(env -u LD_LIBRARY_PATH -u LD_PRELOAD ldd "$elf")
    printf '%s\n' "$result" >> "$stage/checks/ldd.txt"
    [[ $result != *'not found'* ]] || die "unresolved runtime dependency: $elf"
    dependency_pattern='^[[:space:]]*([^[:space:]]+)[[:space:]]+=>[[:space:]]+(.*)[[:space:]]+\(0x[[:xdigit:]]+\)$'
    while IFS= read -r line; do
        [[ $line =~ $dependency_pattern ]] || continue
        name=${BASH_REMATCH[1]} resolved=${BASH_REMATCH[2]}
        case "$name" in
            libginkgo-kws.so|libsherpa-onnx-c-api.so|libonnxruntime.so)
                resolved=$(realpath -e -- "$resolved")
                [[ $resolved == "$(realpath -e -- "$stage/lib/$name")" ]] || die "dependency escaped bundle: $name => $resolved" ;;
            libc.so.*|libm.so.*|libstdc++.so.*|libgcc_s.so.*|libdl.so.*|librt.so.*|libpthread.so.*) ;;
            /lib/ld-linux*.so.*|/lib64/ld-linux*.so.*|ld-linux*.so.*) ;;
            *) die "unpackaged non-system runtime dependency: $name" ;;
        esac
    done <<< "$result"
done
env -u LD_LIBRARY_PATH -u LD_PRELOAD timeout 10 "$stage/bin/ginkgo-voice" --help > "$stage/checks/voice-help.txt" 2>&1
if ! "$helper_only"; then
    # --help is read-only: never launch the daemon or microphone during packaging.
    "$stage/bin/ginkgo-cli" --help > "$stage/checks/cli-help.txt"
    if readelf -l "$stage/bin/ginkgo-cli" | grep -q INTERP; then
        die "CLI unexpectedly requires a dynamic loader"
    fi
fi
cp -- "$source_dir/LICENSE" "$stage/licenses/sherpa-onnx-LICENSE"
cp -- "$ort_prefix/LICENSE" "$stage/licenses/onnxruntime-LICENSE"
cp -- "$ort_prefix/ThirdPartyNotices.txt" "$stage/licenses/onnxruntime-ThirdPartyNotices.txt"
cp -- "$root/docs/voice.md" "$stage/README.md"
cat > "$stage/voice-sherpa.example.toml" <<'EOF'
# Merge manually into your existing config. Capture remains disabled here.
# Run bin/ginkgo-cli voice setup to provision and verify the pinned models.
# The helper is discovered beside the CLI; keep bin/ and lib/ together.
# Then enable voice below, choose raw or ai, and restart the bundled daemon.
# Start capture with: bin/ginkgo-cli voice stream --save
# Say "Hey Ginkgo", then dictate. See README.md for advanced overrides.
[voice]
enabled = false
mode = "raw"
EOF
{
    printf 'ginkgo_revision=%s\n' "$(git rev-parse HEAD)"
    printf 'ginkgo_worktree_dirty=%s\n' "$(test -z "$(git status --porcelain)" && echo false || echo true)"
    printf 'sherpa_version=1.13.8\nsherpa_revision=%s\nonnxruntime_version=1.28.2\nprovider=cpu\n' "$revision"
    printf 'go=%s\n' "$(go version)"
    printf 'cmake=%s\n' "$(cmake --version | sed -n '1p')"
    printf 'cxx=%s\n' "$("${CXX:-c++}" --version | sed -n '1p')"
    printf 'machine=%s\n' "$(uname -m)"
    printf 'native_tags=sherpa_onnx\ngo_flags=-trimpath\nhelper_cgo=1\ncli_cgo=0\n'
    printf 'models=external; not included, downloaded or validated by packaging\n'
    printf 'ginkgod_symlink=bin/ginkgod -> ginkgo-cli (full bundle only)\n'
    printf 'system_runtime=Linux ELF loader, glibc, libstdc++, libgcc_s (compatible host required)\n'
} > "$stage/BUILD-MANIFEST.txt"
# Remove only our temporary shim build, not external upstream build artifacts.
rm -rf -- "$stage/.native-build"
(
    cd -- "$stage"
    find bin lib licenses -type f -print0 | sort -z | xargs -0 sha256sum
    sha256sum BUILD-MANIFEST.txt README.md voice-sherpa.example.toml
) > "$stage/SHA256SUMS"
[[ ! -e $output && ! -L $output ]] || die "output appeared during build: $output"
mv -T -n -- "$stage" "$output"
[[ ! -d $stage ]] || die "output appeared during publish; existing output was retained: $output"
trap - EXIT
echo "Built local CPU bundle: $output"
