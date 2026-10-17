#!/usr/bin/env bash
# Input/failure and tiny real cgo regression: no downloads, model or mic access.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
scratch=$(mktemp -d /tmp/opencode/ginkgo-package-test.XXXXXXXX)
trap 'rm -rf -- "$scratch"' EXIT
script=$root/scripts/build-voice-bundle.sh
bash -n "$script"
bash "$script" --help > "$scratch/help"
grep -q 'No downloads' "$scratch/help"
expect_failure() {
    local expected=$1
    shift
    if bash "$script" "$@" > "$scratch/stdout" 2> "$scratch/stderr"; then
        echo "unexpected success: $*" >&2
        exit 1
    fi
    grep -F -- "$expected" "$scratch/stderr" >/dev/null || {
        cat "$scratch/stderr" >&2
        exit 1
    }
}
expect_failure 'unknown argument' --bogus
expect_failure 'missing value' --output
expect_failure 'missing value' --output --helper-only
expect_failure 'are required' --output "$scratch/untouched output"
expect_failure 'directories are missing' --output "$scratch/untouched output" \
    --sherpa-source "$scratch/no source" --sherpa-build "$scratch/no build"
[[ ! -e $scratch/untouched\ output ]]
mkdir "$scratch/existing output"
printf 'retain me\n' > "$scratch/existing output/sentinel"
expect_failure 'output already exists' --output "$scratch/existing output" \
    --sherpa-source "$scratch/no source" --sherpa-build "$scratch/no build"
[[ $(cat "$scratch/existing output/sentinel") == 'retain me' ]]
ln -s "$scratch/missing destination" "$scratch/existing symlink"
expect_failure 'output already exists' --output "$scratch/existing symlink" \
    --sherpa-source "$scratch/no source" --sherpa-build "$scratch/no build"
[[ -L $scratch/existing\ symlink ]]
expect_failure 'outside the source tree' --output "$root/build/should-not-exist" \
    --sherpa-source "$scratch/no source" --sherpa-build "$scratch/no build"
fixture=$scratch/'cgo fixture with spaces'
mkdir -p "$fixture/include directory" "$fixture/lib" "$fixture/bin"
printf 'int fixture_value(void);\n' > "$fixture/include directory/fixture.h"
printf 'int fixture_value(void) { return 42; }\n' > "$fixture/fixture.c"
"${CC:-cc}" -shared -fPIC -o "$fixture/lib/libfixture.so" "$fixture/fixture.c"
cat > "$fixture/main.go" <<'EOF'
package main
/*
#cgo LDFLAGS: -lfixture
#include <fixture.h>
*/
import "C"
func main() {
    if C.fixture_value() != 42 { panic("incorrect native fixture result") }
}
EOF
source "$root/scripts/build-voice-bundle-flags.sh"
voice_cgo_flags "$fixture/include directory" "$fixture/include directory" "$fixture/lib"
CGO_ENABLED=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
    go build -o "$fixture/bin/fixture" "$fixture/main.go"
env -u LD_LIBRARY_PATH -u LD_PRELOAD "$fixture/bin/fixture"
if voice_cgo_flags "$fixture/unsupported\"path" "$fixture" "$fixture" 2>/dev/null; then
    echo 'unsupported quote in native path was accepted' >&2
    exit 1
fi
echo 'voice bundle input/failure and real cgo space-path tests passed'
