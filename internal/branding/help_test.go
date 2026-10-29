package branding

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestHelpBrandsWithoutChangingLegacyCommand(t *testing.T) {
	flags := flag.NewFlagSet("gosshd-agent", flag.ContinueOnError)
	var output bytes.Buffer
	flags.SetOutput(&output)
	flags.Bool("desktop-disable", false, "disable desktop RPC")
	SetUsage(flags, "LinkSphere Agent")
	if output.Len() != 0 {
		t.Fatal("help registration wrote a startup banner")
	}
	if err := flags.Parse([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error: %v", err)
	}
	for _, want := range []string{"LinkSphere Agent", "Usage: gosshd-agent [options]", "-desktop-disable"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing help content %q", want)
		}
	}
}
