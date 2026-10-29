package branding

import (
	"flag"
	"fmt"
)

// SetUsage brands help output without changing the executable name or writing
// a startup banner to a protocol stream. Existing flags remain unchanged.
func SetUsage(flags *flag.FlagSet, product string) {
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "%s\nUsage: %s [options]\n", product, flags.Name())
		flags.PrintDefaults()
	}
}
