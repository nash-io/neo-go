package compiler_test

import (
	"math/big"
	"strings"
	"testing"

	"github.com/nspcc-dev/neo-go/pkg/compiler"
	"github.com/nspcc-dev/neo-go/pkg/vm/stackitem"
	"github.com/stretchr/testify/require"
)

var sliceTestCases = []testCase{
	{
		"constant index",
		`
		package foo
		func Main() int {
			a := []int{0,0}
			a[1] = 42
			return a[1]+0
		}
		`,
		big.NewInt(42),
	},
	{
		"variable index",
		`
		package foo
		func Main() int {
			a := []int{0,0}
			i := 1
			a[i] = 42
			return a[1]+0
		}
		`,
		big.NewInt(42),
	},
	{
		"increase slice element with +=",
		`package foo
		func Main() int {
			a := []int{1, 2, 3}
			a[1] += 40
			return a[1]
		}`,
		big.NewInt(42),
	},
	{
		"complex test",
		`
		package foo
		func Main() int {
			a := []int{1,2,3}
			x := a[0]
			a[x] = a[x] + 4
			a[x] = a[x] + a[2]
			return a[1]
		}
		`,
		big.NewInt(9),
	},
	{
		"slice literals with variables",
		`
		package foo
		func Main() int {
			elem := 7
			a := []int{6, elem, 8}
			return a[1]
		}
		`,
		big.NewInt(7),
	},
	{
		"slice literals with expressions",
		`
		package foo
		func Main() int {
			elem := []int{3, 7}
			a := []int{6, elem[1]*2+1, 24}
			return a[1]
		}
		`,
		big.NewInt(15),
	},
	{
		"sub-slice with literal bounds",
		`
		package foo
		func Main() []byte {
			a := []byte{0, 1, 2, 3}
			b := a[1:3]
			return b
		}`,
		[]byte{1, 2},
	},
	{
		"sub-slice with constant bounds",
		`
		package foo
		const x = 1
		const y = 3
		func Main() []byte {
			a := []byte{0, 1, 2, 3}
			b := a[x:y]
			return b
		}`,
		[]byte{1, 2},
	},
	{
		"sub-slice with variable bounds",
		`
		package foo
		func Main() []byte {
			a := []byte{0, 1, 2, 3}
			x := 1
			y := 3
			b := a[x:y]
			return b
		}`,
		[]byte{1, 2},
	},
	{
		"sub-slice with no lower bound",
		`
		package foo
		func Main() []byte {
			a := []byte{0, 1, 2, 3}
			b := a[:3]
			return b
		}`,
		[]byte{0, 1, 2},
	},
	{
		"sub-slice with no upper bound",
		`
		package foo
		func Main() []byte {
			a := []byte{0, 1, 2, 3}
			b := a[2:]
			return b
		}`,
		[]byte{2, 3},
	},
	{
		"declare byte slice",
		`package foo
		func Main() []byte {
			var a []byte
			a = append(a, 1)
			a = append(a, 2)
			return a
		}`,
		[]byte{1, 2},
	},
	{
		"append multiple bytes to a slice",
		`package foo
		func Main() []byte {
			var a []byte
			a = append(a, 1, 2)
			return a
		}`,
		[]byte{1, 2},
	},
	{
		"append multiple ints to a slice",
		`package foo
		func Main() []int {
			var a []int
			a = append(a, 1, 2, 3)
			a = append(a, 4, 5)
			return a
		}`,
		[]stackitem.Item{
			stackitem.NewBigInteger(big.NewInt(1)),
			stackitem.NewBigInteger(big.NewInt(2)),
			stackitem.NewBigInteger(big.NewInt(3)),
			stackitem.NewBigInteger(big.NewInt(4)),
			stackitem.NewBigInteger(big.NewInt(5)),
		},
	},
	{
		"declare compound slice",
		`package foo
		func Main() []string {
			var a []string
			a = append(a, "a")
			a = append(a, "b")
			return a
		}`,
		[]stackitem.Item{
			stackitem.NewByteArray([]byte("a")),
			stackitem.NewByteArray([]byte("b")),
		},
	},
	{
		"declare compound slice alias",
		`package foo
		type strs []string
		func Main() []string {
			var a strs
			a = append(a, "a")
			a = append(a, "b")
			return a
		}`,
		[]stackitem.Item{
			stackitem.NewByteArray([]byte("a")),
			stackitem.NewByteArray([]byte("b")),
		},
	},
	{
		"byte-slice assignment",
		`package foo
		func Main() []byte {
			a := []byte{0, 1, 2}
			a[1] = 42
			return a
		}`,
		[]byte{0, 42, 2},
	},
	{
		"byte-slice assignment after string conversion",
		`package foo
		func Main() []byte {
			a := "abc"
			b := []byte(a)
			b[1] = 42
			return []byte(a)
		}`,
		[]byte{0x61, 0x62, 0x63},
	},
	{
		"declare and append byte-slice",
		`package foo
		func Main() []byte {
			var a []byte
			a = append(a, 1)
			a = append(a, 2)
			return a
		}`,
		[]byte{1, 2},
	},
	{
		"nested slice assignment",
		`package foo
		func Main() int {
			a := [][]int{[]int{1, 2}, []int{3, 4}}
			a[1][0] = 42
			return a[1][0]
		}`,
		big.NewInt(42),
	},
	{
		"nested slice omitted type (slice)",
		`package foo
		func Main() int {
			a := [][]int{{1, 2}, {3, 4}}
			a[1][0] = 42
			return a[1][0]
		}`,
		big.NewInt(42),
	},
	{
		"nested slice omitted type (struct)",
		`package foo
		type pair struct { a, b int }
		func Main() int {
			a := []pair{{a: 1, b: 2}, {a: 3, b: 4}}
			a[1].a = 42
			return a[1].a
		}`,
		big.NewInt(42),
	},
	{
		"defaults to nil for byte slice",
		`
		package foo
		func Main() int {
			var a []byte
			if a != nil { return 1}
			return 2
		}
		`,
		big.NewInt(2),
	},
	{
		"defaults to nil for int slice",
		`
		package foo
		func Main() int {
			var a []int
			if a != nil { return 1}
			return 2
		}
		`,
		big.NewInt(2),
	},
	{
		"defaults to nil for struct slice",
		`
		package foo
		type pair struct { a, b int }
		func Main() int {
			var a []pair
			if a != nil { return 1}
			return 2
		}
		`,
		big.NewInt(2),
	},
}

func TestSliceOperations(t *testing.T) {
	runTestCases(t, sliceTestCases)
}

func TestJumps(t *testing.T) {
	src := `
	package foo
	func Main() []byte {
		buf := []byte{0x62, 0x01, 0x00}
		return buf
	}
	`
	eval(t, src, []byte{0x62, 0x01, 0x00})
}

func TestMake(t *testing.T) {
	t.Run("Map", func(t *testing.T) {
		src := `package foo
		func Main() int {
			a := make(map[int]int)
			a[1] = 10
			a[2] = 20
			return a[1]
		}`
		eval(t, src, big.NewInt(10))
	})
	t.Run("IntSlice", func(t *testing.T) {
		src := `package foo
		func Main() int {
			a := make([]int, 10)
			return len(a) + a[0]
		}`
		eval(t, src, big.NewInt(10))
	})
	t.Run("ByteSlice", func(t *testing.T) {
		src := `package foo
		func Main() int {
			a := make([]byte, 10)
			return len(a) + int(a[0])
		}`
		eval(t, src, big.NewInt(10))
	})
}

func TestCopy(t *testing.T) {
	t.Run("Invalid", func(t *testing.T) {
		src := `package foo
		func Main() []int {
			src := []int{3, 2, 1}
			dst := make([]int, 2)
			copy(dst, src)
			return dst
		}`
		_, err := compiler.Compile("foo.go", strings.NewReader(src))
		require.Error(t, err)
	})
	t.Run("Simple", func(t *testing.T) {
		src := `package foo
		func Main() []byte {
			src := []byte{3, 2, 1}
			dst := make([]byte, 2)
			copy(dst, src)
			return dst
		}`
		eval(t, src, []byte{3, 2})
	})
	t.Run("LowSrcIndex", func(t *testing.T) {
		src := `package foo
		func Main() []byte {
			src := []byte{3, 2, 1}
			dst := make([]byte, 2)
			copy(dst, src[1:])
			return dst
		}`
		eval(t, src, []byte{2, 1})
	})
	t.Run("LowDstIndex", func(t *testing.T) {
		src := `package foo
		func Main() []byte {
			src := []byte{3, 2, 1}
			dst := make([]byte, 2)
			copy(dst[1:], src[1:])
			return dst
		}`
		eval(t, src, []byte{0, 2})
	})
	t.Run("BothIndices", func(t *testing.T) {
		src := `package foo
		func Main() []byte {
			src := []byte{4, 3, 2, 1}
			dst := make([]byte, 4)
			copy(dst[1:], src[1:3])
			return dst
		}`
		eval(t, src, []byte{0, 3, 2, 0})
	})
	t.Run("EmptySliceExpr", func(t *testing.T) {
		src := `package foo
		func Main() []byte {
			src := []byte{3, 2, 1}
			dst := make([]byte, 2)
			copy(dst[1:], src[:])
			return dst
		}`
		eval(t, src, []byte{0, 3})
	})
}
