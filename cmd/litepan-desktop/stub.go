//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "litepan-desktop 只支持 Windows；其他平台请用 cmd/litepan。")
	os.Exit(1)
}
