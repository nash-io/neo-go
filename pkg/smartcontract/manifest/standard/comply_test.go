package standard

import (
	"errors"
	"testing"

	"github.com/nspcc-dev/neo-go/pkg/smartcontract"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract/manifest"
	"github.com/stretchr/testify/require"
)

func fooMethodBarEvent() *manifest.Manifest {
	return &manifest.Manifest{
		ABI: manifest.ABI{
			Methods: []manifest.Method{
				{
					Name: "foo",
					Parameters: []manifest.Parameter{
						{Type: smartcontract.ByteArrayType},
						{Type: smartcontract.PublicKeyType},
					},
					ReturnType: smartcontract.IntegerType,
				},
			},
			Events: []manifest.Event{
				{
					Name: "bar",
					Parameters: []manifest.Parameter{
						{Type: smartcontract.StringType},
					},
				},
			},
		},
	}
}

func TestComplyMissingMethod(t *testing.T) {
	m := fooMethodBarEvent()
	m.ABI.GetMethod("foo").Name = "notafoo"
	err := Comply(m, fooMethodBarEvent())
	require.True(t, errors.Is(err, ErrMethodMissing))
}

func TestComplyInvalidReturnType(t *testing.T) {
	m := fooMethodBarEvent()
	m.ABI.GetMethod("foo").ReturnType = smartcontract.VoidType
	err := Comply(m, fooMethodBarEvent())
	require.True(t, errors.Is(err, ErrInvalidReturnType))
}

func TestComplyMethodParameterCount(t *testing.T) {
	t.Run("Method", func(t *testing.T) {
		m := fooMethodBarEvent()
		f := m.ABI.GetMethod("foo")
		f.Parameters = append(f.Parameters, manifest.Parameter{Type: smartcontract.BoolType})
		err := Comply(m, fooMethodBarEvent())
		require.True(t, errors.Is(err, ErrInvalidParameterCount))
	})
	t.Run("Event", func(t *testing.T) {
		m := fooMethodBarEvent()
		ev := m.ABI.GetEvent("bar")
		ev.Parameters = append(ev.Parameters[:0])
		err := Comply(m, fooMethodBarEvent())
		require.True(t, errors.Is(err, ErrInvalidParameterCount))
	})
}

func TestComplyParameterType(t *testing.T) {
	t.Run("Method", func(t *testing.T) {
		m := fooMethodBarEvent()
		m.ABI.GetMethod("foo").Parameters[0].Type = smartcontract.InteropInterfaceType
		err := Comply(m, fooMethodBarEvent())
		require.True(t, errors.Is(err, ErrInvalidParameterType))
	})
	t.Run("Event", func(t *testing.T) {
		m := fooMethodBarEvent()
		m.ABI.GetEvent("bar").Parameters[0].Type = smartcontract.InteropInterfaceType
		err := Comply(m, fooMethodBarEvent())
		require.True(t, errors.Is(err, ErrInvalidParameterType))
	})
}

func TestMissingEvent(t *testing.T) {
	m := fooMethodBarEvent()
	m.ABI.GetEvent("bar").Name = "notabar"
	err := Comply(m, fooMethodBarEvent())
	require.True(t, errors.Is(err, ErrEventMissing))
}

func TestComplyValid(t *testing.T) {
	m := fooMethodBarEvent()
	m.ABI.Methods = append(m.ABI.Methods, manifest.Method{
		Name:       "newmethod",
		Offset:     123,
		ReturnType: smartcontract.ByteArrayType,
	})
	m.ABI.Events = append(m.ABI.Events, manifest.Event{
		Name: "otherevent",
		Parameters: []manifest.Parameter{{
			Name: "names do not matter",
			Type: smartcontract.IntegerType,
		}},
	})
	require.NoError(t, Comply(m, fooMethodBarEvent()))
}
