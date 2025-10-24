package main

import (
    "log"

    "github.com/mithrel/ginkgo/internal/cli"
)

func main() {
    if err := cli.Execute(); err != nil {
        log.Fatal(err)
    }
}

