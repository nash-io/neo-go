package flags

import (
	"testing"

	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestFixed8_String(t *testing.T) {
	value := util.Fixed8(123)
	f := Fixed8{
		Value: value,
	}

	require.Equal(t, "0.00000123", f.String())
}

func TestFixed8_Set(t *testing.T) {
	value := util.Fixed8(123)
	f := Fixed8{}

	require.Error(t, f.Set("not-a-fixed8"))

	require.NoError(t, f.Set("0.00000123"))
	require.Equal(t, value, f.Value)
}

func TestFixed8_Fixed8(t *testing.T) {
	f := Fixed8{
		Value: util.Fixed8(123),
	}

	require.Equal(t, util.Fixed8(123), f.Fixed8())
}

func TestFixed8Flag_String(t *testing.T) {
	flag := Fixed8Flag{
		Name:  "myFlag",
		Usage: "Gas amount",
	}

	require.Equal(t, "--myFlag value\tGas amount", flag.String())
}

func TestFixed8Flag_GetName(t *testing.T) {
	flag := Fixed8Flag{
		Name: "myFlag",
	}

	require.Equal(t, "myFlag", flag.GetName())
}
