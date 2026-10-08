// fakemanager answers --version as the manager does, for the self-update test.
package main

import (
	"fmt"
	"os"
)

var (
	product = "ReSkateManager"
	version = "dev"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(product, version)
	}
}
