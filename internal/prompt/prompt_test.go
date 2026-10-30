package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLine(t *testing.T) {
	for input, want := range map[string]string{
		"secret\n":        "secret",
		"secret\r\n":      "secret",
		"secret":          "secret",
		" spaced out \n":  " spaced out ",
		"first\nsecond\n": "first",
		"":                "",
	} {
		got, err := Line(strings.NewReader(input))
		require.NoError(t, err, "input %q", input)
		require.Equal(t, want, got, "input %q", input)
	}
}
