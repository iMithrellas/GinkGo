# Shared by the packager and its real cgo compile regression.
voice_cgo_flags() {
    local path
    for path in "$@"; do
        case "$path" in
            *\"*|*\'*|*$'\n'*|*$'\r'*)
                echo 'native flag paths cannot contain quotes or newlines' >&2
                return 1 ;;
        esac
    done
    # Go's quoted.Split requires quotes at the start of the entire argument.
    export CGO_CFLAGS="${CGO_CFLAGS:-} \"-I$1\" \"-I$2\""
    export CGO_LDFLAGS="${CGO_LDFLAGS:-} \"-L$3\" -Wl,--disable-new-dtags -Wl,-rpath,\$ORIGIN/../lib"
}
