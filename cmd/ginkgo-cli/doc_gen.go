//go:build ignore
// +build ignore

package main

import (
	"log"

	ginkgo "github.com/mithrel/ginkgo/internal/cli"
	"github.com/spf13/cobra/doc"
)

func main() {
	root := ginkgo.NewRootCmd()

	if err := doc.GenMarkdownTree(root, "./docs/markdown"); err != nil {
		log.Fatal(err)
	}

	header := &doc.GenManHeader{
		Title:   "GINKGO-CLI",
		Section: "1",
	}
	if err := doc.GenManTree(root, header, "./docs/man"); err != nil {
		log.Fatal(err)
	}
}
