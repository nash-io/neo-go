package standard

import (
	"errors"
	"fmt"

	"github.com/nspcc-dev/neo-go/pkg/smartcontract/manifest"
)

var (
	ErrMethodMissing         = errors.New("method missing")
	ErrEventMissing          = errors.New("event missing")
	ErrInvalidReturnType     = errors.New("invalid return type")
	ErrInvalidParameterCount = errors.New("invalid parameter count")
	ErrInvalidParameterType  = errors.New("invalid parameter type")
)

// Comply if m has all methods and event from st manifest and they have the same signature.
// Parameter names are ignored.
func Comply(m, st *manifest.Manifest) error {
	for _, stm := range st.ABI.Methods {
		name := stm.Name
		md := m.ABI.GetMethod(name)
		if md == nil {
			return fmt.Errorf("%w: '%s'", ErrMethodMissing, name)
		} else if stm.ReturnType != md.ReturnType {
			return fmt.Errorf("%w: '%s' (expected %s, got %s)", ErrInvalidReturnType,
				name, stm.ReturnType, md.ReturnType)
		} else if len(stm.Parameters) != len(md.Parameters) {
			return fmt.Errorf("%w: '%s' (expected %d, got %d)", ErrInvalidParameterCount,
				name, len(stm.Parameters), len(md.Parameters))
		}
		for i := range stm.Parameters {
			if stm.Parameters[i].Type != md.Parameters[i].Type {
				return fmt.Errorf("%w: '%s'[%d] (expected %s, got %s)", ErrInvalidParameterType,
					name, i, stm.Parameters[i].Type, md.Parameters[i].Type)
			}
		}
	}
	for _, ste := range st.ABI.Events {
		name := ste.Name
		ed := m.ABI.GetEvent(name)
		if ed == nil {
			return fmt.Errorf("%w: event '%s'", ErrEventMissing, name)
		} else if len(ste.Parameters) != len(ed.Parameters) {
			return fmt.Errorf("%w: event '%s' (expected %d, got %d)", ErrInvalidParameterCount,
				name, len(ste.Parameters), len(ed.Parameters))
		}
		for i := range ste.Parameters {
			if ste.Parameters[i].Type != ed.Parameters[i].Type {
				return fmt.Errorf("%w: event '%s' (expected %s, got %s)", ErrInvalidParameterType,
					name, ste.Parameters[i].Type, ed.Parameters[i].Type)
			}
		}
	}
	return nil
}
