// Command iormesheval runs the LLM introspection eval (PLAN.md M5):
// a question set over a scenario's collected trace, put to the model
// command the operator names. `mage eval` runs it; it is never part of
// `mage test`. All logic lives in internal/eval.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"iormesh/internal/eval"
	"iormesh/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-version" {
		fmt.Println(version.Version)
		return
	}
	o, err := eval.ParseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(2)
	}
	if err == nil {
		err = eval.Main(context.Background(), o, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "iormesheval: %v\n", err)
		os.Exit(1)
	}
}
