package compiler

import (
	"encoding/binary"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"math"
	"sort"
	"strings"

	"github.com/nspcc-dev/neo-go/pkg/core/interop/interopnames"
	"github.com/nspcc-dev/neo-go/pkg/encoding/address"
	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/vm"
	"github.com/nspcc-dev/neo-go/pkg/vm/emit"
	"github.com/nspcc-dev/neo-go/pkg/vm/opcode"
	"github.com/nspcc-dev/neo-go/pkg/vm/stackitem"
	"golang.org/x/tools/go/loader"
)

type codegen struct {
	// Information about the program with all its dependencies.
	buildInfo *buildInfo

	// prog holds the output buffer.
	prog *io.BufBinWriter

	// Type information.
	typeInfo *types.Info

	// A mapping of func identifiers with their scope.
	funcs map[string]*funcScope

	// A mapping of lambda functions into their scope.
	lambda map[string]*funcScope

	// Current funcScope being converted.
	scope *funcScope

	globals map[string]int

	// A mapping from label's names to their ids.
	labels map[labelWithType]uint16
	// A list of nested label names together with evaluation stack depth.
	labelList []labelWithStackSize

	// A label for the for-loop being currently visited.
	currentFor string
	// A label for the switch statement being visited.
	currentSwitch string
	// A label to be used in the next statement.
	nextLabel string

	// sequencePoints is mapping from method name to a slice
	// containing info about mapping from opcode's offset
	// to a text span in the source file.
	sequencePoints map[string][]DebugSeqPoint

	// initEndOffset specifies the end of the initialization method.
	initEndOffset int

	// importMap contains mapping from package aliases to full package names for the current file.
	importMap map[string]string

	// constMap contains constants from foreign packages.
	constMap map[string]types.TypeAndValue

	// currPkg is current package being processed.
	currPkg *types.Package

	// mainPkg is a main package metadata.
	mainPkg *loader.PackageInfo

	// packages contains packages in the order they were loaded.
	packages []string

	// documents contains paths to all files used by the program.
	documents []string
	// docIndex maps file path to an index in documents array.
	docIndex map[string]int

	// Label table for recording jump destinations.
	l []int
}

type labelOffsetType byte

const (
	labelStart labelOffsetType = iota // labelStart is a default label type
	labelEnd                          // labelEnd is a type for labels that are targets for break
	labelPost                         // labelPost is a type for labels that are targets for continue
)

type labelWithType struct {
	name string
	typ  labelOffsetType
}

type labelWithStackSize struct {
	name string
	sz   int
}

type varType int

const (
	varGlobal varType = iota
	varLocal
	varArgument
)

// newLabel creates a new label to jump to
func (c *codegen) newLabel() (l uint16) {
	li := len(c.l)
	if li > math.MaxUint16 {
		c.prog.Err = errors.New("label number is too big")
		return
	}
	l = uint16(li)
	c.l = append(c.l, -1)
	return
}

// newNamedLabel creates a new label with a specified name.
func (c *codegen) newNamedLabel(typ labelOffsetType, name string) (l uint16) {
	l = c.newLabel()
	lt := labelWithType{name: name, typ: typ}
	c.labels[lt] = l
	return
}

func (c *codegen) setLabel(l uint16) {
	c.l[l] = c.pc() + 1
}

// pc returns the program offset off the last instruction.
func (c *codegen) pc() int {
	return c.prog.Len() - 1
}

func (c *codegen) emitLoadConst(t types.TypeAndValue) {
	if c.prog.Err != nil {
		return
	}

	typ, ok := t.Type.Underlying().(*types.Basic)
	if !ok {
		c.prog.Err = fmt.Errorf("compiler doesn't know how to convert this constant: %v", t)
		return
	}

	switch typ.Kind() {
	case types.Int, types.UntypedInt, types.Uint,
		types.Int8, types.Uint8,
		types.Int16, types.Uint16,
		types.Int32, types.Uint32, types.Int64, types.Uint64:
		val, _ := constant.Int64Val(t.Value)
		emit.Int(c.prog.BinWriter, val)
	case types.String, types.UntypedString:
		val := constant.StringVal(t.Value)
		emit.String(c.prog.BinWriter, val)
	case types.Bool, types.UntypedBool:
		val := constant.BoolVal(t.Value)
		emit.Bool(c.prog.BinWriter, val)
	default:
		c.prog.Err = fmt.Errorf("compiler doesn't know how to convert this basic type: %v", t)
		return
	}
}

func (c *codegen) emitLoadField(i int) {
	emit.Int(c.prog.BinWriter, int64(i))
	emit.Opcode(c.prog.BinWriter, opcode.PICKITEM)
}

func (c *codegen) emitStoreStructField(i int) {
	emit.Int(c.prog.BinWriter, int64(i))
	emit.Opcode(c.prog.BinWriter, opcode.ROT)
	emit.Opcode(c.prog.BinWriter, opcode.SETITEM)
}

// getVarIndex returns variable type and position in corresponding slot,
// according to current scope.
func (c *codegen) getVarIndex(pkg string, name string) (varType, int) {
	if pkg == "" {
		if c.scope != nil {
			vt, val := c.scope.vars.getVarIndex(name)
			if val >= 0 {
				return vt, val
			}
		}
	}
	if i, ok := c.globals[c.getIdentName(pkg, name)]; ok {
		return varGlobal, i
	}

	return varLocal, c.scope.newVariable(varLocal, name)
}

func getBaseOpcode(t varType) (opcode.Opcode, opcode.Opcode) {
	switch t {
	case varGlobal:
		return opcode.LDSFLD0, opcode.STSFLD0
	case varLocal:
		return opcode.LDLOC0, opcode.STLOC0
	case varArgument:
		return opcode.LDARG0, opcode.STARG0
	default:
		panic("invalid type")
	}
}

// emitLoadVar loads specified variable to the evaluation stack.
func (c *codegen) emitLoadVar(pkg string, name string) {
	t, i := c.getVarIndex(pkg, name)
	base, _ := getBaseOpcode(t)
	if i < 7 {
		emit.Opcode(c.prog.BinWriter, base+opcode.Opcode(i))
	} else {
		emit.Instruction(c.prog.BinWriter, base+7, []byte{byte(i)})
	}
}

// emitStoreVar stores top value from the evaluation stack in the specified variable.
func (c *codegen) emitStoreVar(pkg string, name string) {
	if name == "_" {
		emit.Opcode(c.prog.BinWriter, opcode.DROP)
		return
	}
	t, i := c.getVarIndex(pkg, name)
	_, base := getBaseOpcode(t)
	if i < 7 {
		emit.Opcode(c.prog.BinWriter, base+opcode.Opcode(i))
	} else {
		emit.Instruction(c.prog.BinWriter, base+7, []byte{byte(i)})
	}
}

func (c *codegen) emitDefault(t types.Type) {
	switch t := t.Underlying().(type) {
	case *types.Basic:
		info := t.Info()
		switch {
		case info&types.IsInteger != 0:
			emit.Int(c.prog.BinWriter, 0)
		case info&types.IsString != 0:
			emit.Bytes(c.prog.BinWriter, []byte{})
		case info&types.IsBoolean != 0:
			emit.Bool(c.prog.BinWriter, false)
		default:
			emit.Opcode(c.prog.BinWriter, opcode.PUSHNULL)
		}
	case *types.Struct:
		num := t.NumFields()
		emit.Int(c.prog.BinWriter, int64(num))
		emit.Opcode(c.prog.BinWriter, opcode.NEWSTRUCT)
		for i := 0; i < num; i++ {
			emit.Opcode(c.prog.BinWriter, opcode.DUP)
			emit.Int(c.prog.BinWriter, int64(i))
			c.emitDefault(t.Field(i).Type())
			emit.Opcode(c.prog.BinWriter, opcode.SETITEM)
		}
	default:
		emit.Opcode(c.prog.BinWriter, opcode.PUSHNULL)
	}
}

// convertGlobals traverses the AST and only converts global declarations.
// If we call this in convertFuncDecl then it will load all global variables
// into the scope of the function.
func (c *codegen) convertGlobals(f *ast.File, _ *types.Package) {
	ast.Inspect(f, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncDecl:
			return false
		case *ast.GenDecl:
			ast.Walk(c, n)
		}
		return true
	})
}

func isInitFunc(decl *ast.FuncDecl) bool {
	return decl.Name.Name == "init" && decl.Recv == nil &&
		decl.Type.Params.NumFields() == 0 &&
		decl.Type.Results.NumFields() == 0
}

func (c *codegen) convertInitFuncs(f *ast.File, pkg *types.Package) {
	ast.Inspect(f, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncDecl:
			if isInitFunc(n) {
				c.convertFuncDecl(f, n, pkg)
			}
		case *ast.GenDecl:
			return false
		}
		return true
	})
}

func (c *codegen) convertFuncDecl(file ast.Node, decl *ast.FuncDecl, pkg *types.Package) {
	var (
		f            *funcScope
		ok, isLambda bool
	)
	isInit := isInitFunc(decl)
	if isInit {
		f = c.newFuncScope(decl, c.newLabel())
	} else {
		f, ok = c.funcs[c.getFuncNameFromDecl("", decl)]
		if ok {
			// If this function is a syscall or builtin we will not convert it to bytecode.
			if isSyscall(f) || isCustomBuiltin(f) {
				return
			}
			c.setLabel(f.label)
		} else if f, ok = c.lambda[c.getIdentName("", decl.Name.Name)]; ok {
			isLambda = ok
			c.setLabel(f.label)
		} else {
			f = c.newFunc(decl)
		}
	}

	f.rng.Start = uint16(c.prog.Len())
	c.scope = f
	ast.Inspect(decl, c.scope.analyzeVoidCalls) // @OPTIMIZE

	// All globals copied into the scope of the function need to be added
	// to the stack size of the function.
	sizeLoc := f.countLocals()
	if sizeLoc > 255 {
		c.prog.Err = errors.New("maximum of 255 local variables is allowed")
	}
	sizeArg := f.countArgs()
	if sizeArg > 255 {
		c.prog.Err = errors.New("maximum of 255 local variables is allowed")
	}
	if sizeLoc != 0 || sizeArg != 0 {
		emit.Instruction(c.prog.BinWriter, opcode.INITSLOT, []byte{byte(sizeLoc), byte(sizeArg)})
	}

	f.vars.newScope()
	defer f.vars.dropScope()

	// We need to handle methods, which in Go, is just syntactic sugar.
	// The method receiver will be passed in as first argument.
	// We check if this declaration has a receiver and load it into scope.
	//
	// FIXME: For now we will hard cast this to a struct. We can later fine tune this
	// to support other types.
	if decl.Recv != nil {
		for _, arg := range decl.Recv.List {
			// only create an argument here, it will be stored via INITSLOT
			c.scope.newVariable(varArgument, arg.Names[0].Name)
		}
	}

	// Load the arguments in scope.
	for _, arg := range decl.Type.Params.List {
		for _, id := range arg.Names {
			// only create an argument here, it will be stored via INITSLOT
			c.scope.newVariable(varArgument, id.Name)
		}
	}

	ast.Walk(c, decl.Body)

	// If we have reached the end of the function without encountering `return` statement,
	// we should clean alt.stack manually.
	// This can be the case with void and named-return functions.
	if !isInit && !lastStmtIsReturn(decl) {
		c.saveSequencePoint(decl.Body)
		emit.Opcode(c.prog.BinWriter, opcode.RET)
	}

	f.rng.End = uint16(c.prog.Len() - 1)

	if !isLambda {
		for _, f := range c.lambda {
			c.convertFuncDecl(file, f.decl, pkg)
		}
		c.lambda = make(map[string]*funcScope)
	}
}

func (c *codegen) Visit(node ast.Node) ast.Visitor {
	if c.prog.Err != nil {
		return nil
	}
	switch n := node.(type) {

	// General declarations.
	// var (
	//     x = 2
	// )
	case *ast.GenDecl:
		if n.Tok == token.VAR || n.Tok == token.CONST {
			c.saveSequencePoint(n)
		}
		if n.Tok == token.CONST {
			for _, spec := range n.Specs {
				vs := spec.(*ast.ValueSpec)
				for i := range vs.Names {
					c.constMap[c.getIdentName("", vs.Names[i].Name)] = c.typeAndValueOf(vs.Values[i])
				}
			}
			return nil
		}
		for _, spec := range n.Specs {
			switch t := spec.(type) {
			case *ast.ValueSpec:
				for _, id := range t.Names {
					if c.scope == nil {
						// it is a global declaration
						c.newGlobal("", id.Name)
					} else {
						c.scope.newLocal(id.Name)
					}
					c.registerDebugVariable(id.Name, t.Type)
				}
				for i := range t.Names {
					if len(t.Values) != 0 {
						ast.Walk(c, t.Values[i])
					} else {
						c.emitDefault(c.typeOf(t.Type))
					}
					c.emitStoreVar("", t.Names[i].Name)
				}
			}
		}
		return nil

	case *ast.AssignStmt:
		multiRet := len(n.Rhs) != len(n.Lhs)
		c.saveSequencePoint(n)
		// Assign operations are grouped https://github.com/golang/go/blob/master/src/go/types/stmt.go#L160
		isAssignOp := token.ADD_ASSIGN <= n.Tok && n.Tok <= token.AND_NOT_ASSIGN
		if isAssignOp {
			// RHS can contain exactly one expression, thus there is no need to iterate.
			ast.Walk(c, n.Lhs[0])
			ast.Walk(c, n.Rhs[0])
			c.emitToken(n.Tok, c.typeOf(n.Rhs[0]))
		}
		for i := 0; i < len(n.Lhs); i++ {
			switch t := n.Lhs[i].(type) {
			case *ast.Ident:
				if n.Tok == token.DEFINE {
					if !multiRet {
						c.registerDebugVariable(t.Name, n.Rhs[i])
					}
					if t.Name != "_" {
						c.scope.newLocal(t.Name)
					}
				}
				if !isAssignOp && (i == 0 || !multiRet) {
					ast.Walk(c, n.Rhs[i])
				}
				c.emitStoreVar("", t.Name)

			case *ast.SelectorExpr:
				if !isAssignOp {
					ast.Walk(c, n.Rhs[i])
				}
				typ := c.typeOf(t.X)
				if typ == nil {
					// Store to other package global variable.
					c.emitStoreVar(t.X.(*ast.Ident).Name, t.Sel.Name)
					return nil
				}
				strct, ok := c.getStruct(typ)
				if !ok {
					c.prog.Err = fmt.Errorf("nested selector assigns not supported yet")
					return nil
				}
				ast.Walk(c, t.X)                      // load the struct
				i := indexOfStruct(strct, t.Sel.Name) // get the index of the field
				c.emitStoreStructField(i)             // store the field

			// Assignments to index expressions.
			// slice[0] = 10
			case *ast.IndexExpr:
				if !isAssignOp {
					ast.Walk(c, n.Rhs[i])
				}
				ast.Walk(c, t.X)
				ast.Walk(c, t.Index)
				emit.Opcode(c.prog.BinWriter, opcode.ROT)
				emit.Opcode(c.prog.BinWriter, opcode.SETITEM)
			}
		}
		return nil

	case *ast.SliceExpr:
		name := n.X.(*ast.Ident).Name
		c.emitLoadVar("", name)

		if n.Low != nil {
			ast.Walk(c, n.Low)
		} else {
			emit.Opcode(c.prog.BinWriter, opcode.PUSH0)
		}

		if n.High != nil {
			ast.Walk(c, n.High)
		} else {
			emit.Opcode(c.prog.BinWriter, opcode.OVER)
			emit.Opcode(c.prog.BinWriter, opcode.SIZE)
		}

		emit.Opcode(c.prog.BinWriter, opcode.OVER)
		emit.Opcode(c.prog.BinWriter, opcode.SUB)
		emit.Opcode(c.prog.BinWriter, opcode.SUBSTR)

		return nil

	case *ast.ReturnStmt:
		l := c.newLabel()
		c.setLabel(l)

		cnt := 0
		for i := range c.labelList {
			cnt += c.labelList[i].sz
		}
		c.dropItems(cnt)

		if len(n.Results) == 0 {
			results := c.scope.decl.Type.Results
			if results.NumFields() != 0 {
				// function with named returns
				for i := len(results.List) - 1; i >= 0; i-- {
					names := results.List[i].Names
					for j := len(names) - 1; j >= 0; j-- {
						c.emitLoadVar("", names[j].Name)
					}
				}
			}
		} else {
			// first result should be on top of the stack
			for i := len(n.Results) - 1; i >= 0; i-- {
				ast.Walk(c, n.Results[i])
			}
		}

		c.saveSequencePoint(n)
		emit.Opcode(c.prog.BinWriter, opcode.RET)
		return nil

	case *ast.IfStmt:
		c.scope.vars.newScope()
		defer c.scope.vars.dropScope()

		lIf := c.newLabel()
		lElse := c.newLabel()
		lElseEnd := c.newLabel()

		if n.Cond != nil {
			c.emitBoolExpr(n.Cond, true, false, lElse)
		}

		c.setLabel(lIf)
		ast.Walk(c, n.Body)
		if n.Else != nil {
			emit.Jmp(c.prog.BinWriter, opcode.JMPL, lElseEnd)
		}

		c.setLabel(lElse)
		if n.Else != nil {
			ast.Walk(c, n.Else)
		}
		c.setLabel(lElseEnd)
		return nil

	case *ast.SwitchStmt:
		ast.Walk(c, n.Tag)

		eqOpcode, _ := convertToken(token.EQL, c.typeOf(n.Tag))
		switchEnd, label := c.generateLabel(labelEnd)

		lastSwitch := c.currentSwitch
		c.currentSwitch = label
		c.pushStackLabel(label, 1)

		startLabels := make([]uint16, len(n.Body.List))
		for i := range startLabels {
			startLabels[i] = c.newLabel()
		}
		for i := range n.Body.List {
			lEnd := c.newLabel()
			lStart := startLabels[i]
			cc := n.Body.List[i].(*ast.CaseClause)

			if l := len(cc.List); l != 0 { // if not `default`
				for j := range cc.List {
					emit.Opcode(c.prog.BinWriter, opcode.DUP)
					ast.Walk(c, cc.List[j])
					emit.Opcode(c.prog.BinWriter, eqOpcode)
					if j == l-1 {
						emit.Jmp(c.prog.BinWriter, opcode.JMPIFNOTL, lEnd)
					} else {
						emit.Jmp(c.prog.BinWriter, opcode.JMPIFL, lStart)
					}
				}
			}

			c.scope.vars.newScope()

			c.setLabel(lStart)
			last := len(cc.Body) - 1
			for j, stmt := range cc.Body {
				if j == last && isFallthroughStmt(stmt) {
					emit.Jmp(c.prog.BinWriter, opcode.JMPL, startLabels[i+1])
					break
				}
				ast.Walk(c, stmt)
			}
			emit.Jmp(c.prog.BinWriter, opcode.JMPL, switchEnd)
			c.setLabel(lEnd)

			c.scope.vars.dropScope()
		}

		c.setLabel(switchEnd)
		c.dropStackLabel()

		c.currentSwitch = lastSwitch

		return nil

	case *ast.FuncLit:
		l := c.newLabel()
		c.newLambda(l, n)
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint16(buf, l)
		emit.Instruction(c.prog.BinWriter, opcode.PUSHA, buf)
		return nil

	case *ast.BasicLit:
		c.emitLoadConst(c.typeAndValueOf(n))
		return nil

	case *ast.StarExpr:
		_, ok := c.getStruct(c.typeOf(n.X))
		if !ok {
			c.prog.Err = errors.New("dereferencing is only supported on structs")
			return nil
		}
		ast.Walk(c, n.X)
		c.emitConvert(stackitem.StructT)
		return nil

	case *ast.Ident:
		if tv := c.typeAndValueOf(n); tv.Value != nil {
			c.emitLoadConst(tv)
		} else if n.Name == "nil" {
			emit.Opcode(c.prog.BinWriter, opcode.PUSHNULL)
		} else {
			c.emitLoadVar("", n.Name)
		}
		return nil

	case *ast.CompositeLit:
		switch typ := c.typeOf(n).Underlying().(type) {
		case *types.Struct:
			c.convertStruct(n, false)
		case *types.Map:
			c.convertMap(n)
		default:
			ln := len(n.Elts)
			// ByteArrays needs a different approach than normal arrays.
			if isByteSlice(typ) {
				c.convertByteArray(n)
				return nil
			}
			for i := ln - 1; i >= 0; i-- {
				ast.Walk(c, n.Elts[i])
			}
			emit.Int(c.prog.BinWriter, int64(ln))
			emit.Opcode(c.prog.BinWriter, opcode.PACK)
		}

		return nil

	case *ast.BinaryExpr:
		c.emitBinaryExpr(n, false, false, 0)
		return nil

	case *ast.CallExpr:
		var (
			f         *funcScope
			ok        bool
			name      string
			numArgs   = len(n.Args)
			isBuiltin bool
			isFunc    bool
			isLiteral bool
		)

		switch fun := n.Fun.(type) {
		case *ast.Ident:
			f, ok = c.funcs[c.getIdentName("", fun.Name)]
			isBuiltin = isGoBuiltin(fun.Name)
			if !ok && !isBuiltin {
				name = fun.Name
			}
			// distinguish lambda invocations from type conversions
			if fun.Obj != nil && fun.Obj.Kind == ast.Var {
				isFunc = true
			}
		case *ast.SelectorExpr:
			// If this is a method call we need to walk the AST to load the struct locally.
			// Otherwise this is a function call from a imported package and we can call it
			// directly.
			name, isMethod := c.getFuncNameFromSelector(fun)
			if isMethod {
				ast.Walk(c, fun.X)
				// Dont forget to add 1 extra argument when its a method.
				numArgs++
			}

			f, ok = c.funcs[name]
			// @FIXME this could cause runtime errors.
			f.selector = fun.X.(*ast.Ident)
			if !ok {
				c.prog.Err = fmt.Errorf("could not resolve function %s", fun.Sel.Name)
				return nil
			}
			isBuiltin = isCustomBuiltin(f)
		case *ast.ArrayType:
			// For now we will assume that there are only byte slice conversions.
			// E.g. []byte("foobar") or []byte(scriptHash).
			ast.Walk(c, n.Args[0])
			c.emitConvert(stackitem.BufferT)
			return nil
		case *ast.FuncLit:
			isLiteral = true
		}

		c.saveSequencePoint(n)

		args := transformArgs(n.Fun, n.Args)

		// Handle the arguments
		for _, arg := range args {
			ast.Walk(c, arg)
			typ := c.typeOf(arg)
			_, ok := typ.Underlying().(*types.Struct)
			if ok && !isInteropPath(typ.String()) {
				// To clone struct fields we create a new array and append struct to it.
				// This way even non-pointer struct fields will be copied.
				emit.Opcode(c.prog.BinWriter, opcode.NEWARRAY0)
				emit.Opcode(c.prog.BinWriter, opcode.DUP)
				emit.Opcode(c.prog.BinWriter, opcode.ROT)
				emit.Opcode(c.prog.BinWriter, opcode.APPEND)
				emit.Opcode(c.prog.BinWriter, opcode.PUSH0)
				emit.Opcode(c.prog.BinWriter, opcode.PICKITEM)
			}
		}
		// Do not swap for builtin functions.
		if !isBuiltin {
			typ, ok := c.typeOf(n.Fun).(*types.Signature)
			if ok && typ.Variadic() && !n.Ellipsis.IsValid() {
				// pack variadic args into an array only if last argument is not of form `...`
				varSize := len(n.Args) - typ.Params().Len() + 1
				c.emitReverse(varSize)
				emit.Int(c.prog.BinWriter, int64(varSize))
				emit.Opcode(c.prog.BinWriter, opcode.PACK)
				numArgs -= varSize - 1
			}
			c.emitReverse(numArgs)
		}

		// Check builtin first to avoid nil pointer on funcScope!
		switch {
		case isBuiltin:
			// Use the ident to check, builtins are not in func scopes.
			// We can be sure builtins are of type *ast.Ident.
			c.convertBuiltin(n)
		case name != "":
			// Function was not found thus is can be only an invocation of func-typed variable or type conversion.
			// We care only about string conversions because all others are effectively no-op in NeoVM.
			// E.g. one cannot write `bool(int(a))`, only `int32(int(a))`.
			if isString(c.typeOf(n.Fun)) {
				c.emitConvert(stackitem.ByteArrayT)
			} else if isFunc {
				c.emitLoadVar("", name)
				emit.Opcode(c.prog.BinWriter, opcode.CALLA)
			}
		case isLiteral:
			ast.Walk(c, n.Fun)
			emit.Opcode(c.prog.BinWriter, opcode.CALLA)
		case isSyscall(f):
			c.convertSyscall(n, f.pkg.Name(), f.name)
		default:
			emit.Call(c.prog.BinWriter, opcode.CALLL, f.label)
		}

		return nil

	case *ast.SelectorExpr:
		typ := c.typeOf(n.X)
		if typ == nil {
			// This is a global variable from a package.
			pkgAlias := n.X.(*ast.Ident).Name
			name := c.getIdentName(pkgAlias, n.Sel.Name)
			if tv, ok := c.constMap[name]; ok {
				c.emitLoadConst(tv)
			} else {
				c.emitLoadVar(pkgAlias, n.Sel.Name)
			}
			return nil
		}
		strct, ok := c.getStruct(typ)
		if !ok {
			c.prog.Err = fmt.Errorf("selectors are supported only on structs")
			return nil
		}
		ast.Walk(c, n.X) // load the struct
		i := indexOfStruct(strct, n.Sel.Name)
		c.emitLoadField(i) // load the field
		return nil

	case *ast.UnaryExpr:
		if n.Op == token.AND {
			// We support only taking address from struct literals.
			// For identifiers we can't support "taking address" in a general way
			// because both struct and array are reference types.
			lit, ok := n.X.(*ast.CompositeLit)
			if ok {
				c.convertStruct(lit, true)
				return nil
			}
			c.prog.Err = fmt.Errorf("'&' can be used only with struct literals")
			return nil
		}

		ast.Walk(c, n.X)
		// From https://golang.org/ref/spec#Operators
		// there can be only following unary operators
		// "+" | "-" | "!" | "^" | "*" | "&" | "<-" .
		// of which last three are not used in SC
		switch n.Op {
		case token.ADD:
			// +10 == 10, no need to do anything in this case
		case token.SUB:
			emit.Opcode(c.prog.BinWriter, opcode.NEGATE)
		case token.NOT:
			emit.Opcode(c.prog.BinWriter, opcode.NOT)
		case token.XOR:
			emit.Opcode(c.prog.BinWriter, opcode.INVERT)
		default:
			c.prog.Err = fmt.Errorf("invalid unary operator: %s", n.Op)
			return nil
		}
		return nil

	case *ast.IncDecStmt:
		ast.Walk(c, n.X)
		c.emitToken(n.Tok, c.typeOf(n.X))

		// For now only identifiers are supported for (post) for stmts.
		// for i := 0; i < 10; i++ {}
		// Where the post stmt is ( i++ )
		if ident, ok := n.X.(*ast.Ident); ok {
			c.emitStoreVar("", ident.Name)
		}
		return nil

	case *ast.IndexExpr:
		// Walk the expression, this could be either an Ident or SelectorExpr.
		// This will load local whatever X is.
		ast.Walk(c, n.X)
		ast.Walk(c, n.Index)
		emit.Opcode(c.prog.BinWriter, opcode.PICKITEM) // just pickitem here

		return nil

	case *ast.BranchStmt:
		var label string
		if n.Label != nil {
			label = n.Label.Name
		} else if n.Tok == token.BREAK {
			label = c.currentSwitch
		} else if n.Tok == token.CONTINUE {
			label = c.currentFor
		}

		cnt := 0
		for i := len(c.labelList) - 1; i >= 0 && c.labelList[i].name != label; i-- {
			cnt += c.labelList[i].sz
		}
		c.dropItems(cnt)

		switch n.Tok {
		case token.BREAK:
			end := c.getLabelOffset(labelEnd, label)
			emit.Jmp(c.prog.BinWriter, opcode.JMPL, end)
		case token.CONTINUE:
			post := c.getLabelOffset(labelPost, label)
			emit.Jmp(c.prog.BinWriter, opcode.JMPL, post)
		}

		return nil

	case *ast.LabeledStmt:
		c.nextLabel = n.Label.Name

		ast.Walk(c, n.Stmt)

		return nil

	case *ast.BlockStmt:
		c.scope.vars.newScope()
		defer c.scope.vars.dropScope()

		for i := range n.List {
			ast.Walk(c, n.List[i])
		}

		return nil

	case *ast.ForStmt:
		c.scope.vars.newScope()
		defer c.scope.vars.dropScope()

		fstart, label := c.generateLabel(labelStart)
		fend := c.newNamedLabel(labelEnd, label)
		fpost := c.newNamedLabel(labelPost, label)

		lastLabel := c.currentFor
		lastSwitch := c.currentSwitch
		c.currentFor = label
		c.currentSwitch = label

		// Walk the initializer and condition.
		if n.Init != nil {
			ast.Walk(c, n.Init)
		}

		// Set label and walk the condition.
		c.pushStackLabel(label, 0)
		c.setLabel(fstart)
		if n.Cond != nil {
			ast.Walk(c, n.Cond)

			// Jump if the condition is false
			emit.Jmp(c.prog.BinWriter, opcode.JMPIFNOTL, fend)
		}

		// Walk body followed by the iterator (post stmt).
		ast.Walk(c, n.Body)
		c.setLabel(fpost)
		if n.Post != nil {
			ast.Walk(c, n.Post)
		}

		// Jump back to condition.
		emit.Jmp(c.prog.BinWriter, opcode.JMPL, fstart)
		c.setLabel(fend)
		c.dropStackLabel()

		c.currentFor = lastLabel
		c.currentSwitch = lastSwitch

		return nil

	case *ast.RangeStmt:
		c.scope.vars.newScope()
		defer c.scope.vars.dropScope()

		start, label := c.generateLabel(labelStart)
		end := c.newNamedLabel(labelEnd, label)
		post := c.newNamedLabel(labelPost, label)

		lastFor := c.currentFor
		lastSwitch := c.currentSwitch
		c.currentFor = label
		c.currentSwitch = label

		ast.Walk(c, n.X)

		// Implementation is a bit different for slices and maps:
		// For slices we iterate index from 0 to len-1, storing array, len and index on stack.
		// For maps we iterate index from 0 to len-1, storing map, keyarray, size and index on stack.
		_, isMap := c.typeOf(n.X).Underlying().(*types.Map)
		emit.Opcode(c.prog.BinWriter, opcode.DUP)
		if isMap {
			emit.Opcode(c.prog.BinWriter, opcode.KEYS)
			emit.Opcode(c.prog.BinWriter, opcode.DUP)
		}
		emit.Opcode(c.prog.BinWriter, opcode.SIZE)
		emit.Opcode(c.prog.BinWriter, opcode.PUSH0)

		stackSize := 3 // slice, len(slice), index
		if isMap {
			stackSize++ // map, keys, len(keys), index in keys
		}
		c.pushStackLabel(label, stackSize)
		c.setLabel(start)

		emit.Opcode(c.prog.BinWriter, opcode.OVER)
		emit.Opcode(c.prog.BinWriter, opcode.OVER)
		emit.Jmp(c.prog.BinWriter, opcode.JMPLEL, end)

		var keyLoaded bool
		needValue := n.Value != nil && n.Value.(*ast.Ident).Name != "_"
		if n.Key != nil && n.Key.(*ast.Ident).Name != "_" {
			if isMap {
				c.rangeLoadKey()
				if needValue {
					emit.Opcode(c.prog.BinWriter, opcode.DUP)
					keyLoaded = true
				}
			} else {
				emit.Opcode(c.prog.BinWriter, opcode.DUP)
			}
			c.emitStoreVar("", n.Key.(*ast.Ident).Name)
		}
		if needValue {
			if !isMap || !keyLoaded {
				c.rangeLoadKey()
			}
			if isMap {
				// we have loaded only key from key array, now load value
				emit.Int(c.prog.BinWriter, 4)
				emit.Opcode(c.prog.BinWriter, opcode.PICK) // load map itself (+1 because key was pushed)
				emit.Opcode(c.prog.BinWriter, opcode.SWAP) // key should be on top
				emit.Opcode(c.prog.BinWriter, opcode.PICKITEM)
			}
			c.emitStoreVar("", n.Value.(*ast.Ident).Name)
		}

		ast.Walk(c, n.Body)

		c.setLabel(post)

		emit.Opcode(c.prog.BinWriter, opcode.INC)
		emit.Jmp(c.prog.BinWriter, opcode.JMPL, start)

		c.setLabel(end)
		c.dropStackLabel()

		c.currentFor = lastFor
		c.currentSwitch = lastSwitch

		return nil

	// We dont really care about assertions for the core logic.
	// The only thing we need is to please the compiler type checking.
	// For this to work properly, we only need to walk the expression
	// not the assertion type.
	case *ast.TypeAssertExpr:
		ast.Walk(c, n.X)
		typ := toNeoType(c.typeOf(n.Type))
		emit.Instruction(c.prog.BinWriter, opcode.CONVERT, []byte{byte(typ)})
		return nil
	}
	return c
}

func (c *codegen) rangeLoadKey() {
	emit.Int(c.prog.BinWriter, 2)
	emit.Opcode(c.prog.BinWriter, opcode.PICK) // load keys
	emit.Opcode(c.prog.BinWriter, opcode.OVER) // load index in key array
	emit.Opcode(c.prog.BinWriter, opcode.PICKITEM)
}

func isFallthroughStmt(c ast.Node) bool {
	s, ok := c.(*ast.BranchStmt)
	return ok && s.Tok == token.FALLTHROUGH
}

func (c *codegen) getCompareWithNilArg(n *ast.BinaryExpr) ast.Expr {
	if isExprNil(n.X) {
		return n.Y
	} else if isExprNil(n.Y) {
		return n.X
	}
	return nil
}

func (c *codegen) emitJumpOnCondition(cond bool, jmpLabel uint16) {
	if cond {
		emit.Jmp(c.prog.BinWriter, opcode.JMPIFL, jmpLabel)
	} else {
		emit.Jmp(c.prog.BinWriter, opcode.JMPIFNOTL, jmpLabel)
	}
}

// emitBoolExpr emits boolean expression. If needJump is true and expression evaluates to `cond`,
// jump to jmpLabel is performed and no item is left on stack.
func (c *codegen) emitBoolExpr(n ast.Expr, needJump bool, cond bool, jmpLabel uint16) {
	if be, ok := n.(*ast.BinaryExpr); ok {
		c.emitBinaryExpr(be, needJump, cond, jmpLabel)
	} else {
		ast.Walk(c, n)
		if needJump {
			c.emitJumpOnCondition(cond, jmpLabel)
		}
	}
}

// emitBinaryExpr emits binary expression. If needJump is true and expression evaluates to `cond`,
// jump to jmpLabel is performed and no item is left on stack.
func (c *codegen) emitBinaryExpr(n *ast.BinaryExpr, needJump bool, cond bool, jmpLabel uint16) {
	// The AST package will try to resolve all basic literals for us.
	// If the typeinfo.Value is not nil we know that the expr is resolved
	// and needs no further action. e.g. x := 2 + 2 + 2 will be resolved to 6.
	// NOTE: Constants will also be automatically resolved be the AST parser.
	// example:
	// const x = 10
	// x + 2 will results into 12
	tinfo := c.typeAndValueOf(n)
	if tinfo.Value != nil {
		c.emitLoadConst(tinfo)
		if needJump && isBool(tinfo.Type) {
			c.emitJumpOnCondition(cond, jmpLabel)
		}
		return
	} else if arg := c.getCompareWithNilArg(n); arg != nil {
		ast.Walk(c, arg)
		emit.Opcode(c.prog.BinWriter, opcode.ISNULL)
		if needJump {
			c.emitJumpOnCondition(cond == (n.Op == token.EQL), jmpLabel)
		} else if n.Op == token.NEQ {
			emit.Opcode(c.prog.BinWriter, opcode.NOT)
		}
		return
	}

	switch n.Op {
	case token.LAND, token.LOR:
		end := c.newLabel()

		// true || .. == true, false && .. == false
		condShort := n.Op == token.LOR
		if needJump {
			l := end
			if cond == condShort {
				l = jmpLabel
			}
			c.emitBoolExpr(n.X, true, condShort, l)
			c.emitBoolExpr(n.Y, true, cond, jmpLabel)
		} else {
			push := c.newLabel()
			c.emitBoolExpr(n.X, true, condShort, push)
			c.emitBoolExpr(n.Y, false, false, 0)
			emit.Jmp(c.prog.BinWriter, opcode.JMPL, end)
			c.setLabel(push)
			emit.Bool(c.prog.BinWriter, condShort)
		}
		c.setLabel(end)

	default:
		ast.Walk(c, n.X)
		ast.Walk(c, n.Y)
		typ := c.typeOf(n.X)
		if !needJump {
			c.emitToken(n.Op, typ)
			return
		}
		op, ok := getJumpForToken(n.Op, typ)
		if !ok {
			c.emitToken(n.Op, typ)
			c.emitJumpOnCondition(cond, jmpLabel)
			return
		}
		if !cond {
			op = negateJmp(op)
		}
		emit.Jmp(c.prog.BinWriter, op, jmpLabel)
	}
}

func (c *codegen) pushStackLabel(name string, size int) {
	c.labelList = append(c.labelList, labelWithStackSize{
		name: name,
		sz:   size,
	})
}

func (c *codegen) dropStackLabel() {
	last := len(c.labelList) - 1
	c.dropItems(c.labelList[last].sz)
	c.labelList = c.labelList[:last]
}

func (c *codegen) dropItems(n int) {
	if n < 4 {
		for i := 0; i < n; i++ {
			emit.Opcode(c.prog.BinWriter, opcode.DROP)
		}
		return
	}

	emit.Int(c.prog.BinWriter, int64(n))
	emit.Opcode(c.prog.BinWriter, opcode.PACK)
	emit.Opcode(c.prog.BinWriter, opcode.DROP)
}

// emitReverse reverses top num items of the stack.
func (c *codegen) emitReverse(num int) {
	switch num {
	case 0, 1:
	case 2:
		emit.Opcode(c.prog.BinWriter, opcode.SWAP)
	case 3:
		emit.Opcode(c.prog.BinWriter, opcode.REVERSE3)
	case 4:
		emit.Opcode(c.prog.BinWriter, opcode.REVERSE4)
	default:
		emit.Int(c.prog.BinWriter, int64(num))
		emit.Opcode(c.prog.BinWriter, opcode.REVERSEN)
	}
}

// generateLabel returns a new label.
func (c *codegen) generateLabel(typ labelOffsetType) (uint16, string) {
	name := c.nextLabel
	if name == "" {
		name = fmt.Sprintf("@%d", len(c.l))
	}

	c.nextLabel = ""
	return c.newNamedLabel(typ, name), name
}

func (c *codegen) getLabelOffset(typ labelOffsetType, name string) uint16 {
	return c.labels[labelWithType{name: name, typ: typ}]
}

// For `&&` and `||` it return an opcode which jumps only if result is known:
// false && .. == false, true || .. = true
func getJumpForToken(tok token.Token, typ types.Type) (opcode.Opcode, bool) {
	switch tok {
	case token.GTR:
		return opcode.JMPGTL, true
	case token.GEQ:
		return opcode.JMPGEL, true
	case token.LSS:
		return opcode.JMPLTL, true
	case token.LEQ:
		return opcode.JMPLEL, true
	case token.EQL, token.NEQ:
		if isNumber(typ) {
			if tok == token.EQL {
				return opcode.JMPEQL, true
			}
			return opcode.JMPNEL, true
		}
	}
	return 0, false
}

// getByteArray returns byte array value from constant expr.
// Only literals are supported.
func (c *codegen) getByteArray(expr ast.Expr) []byte {
	switch t := expr.(type) {
	case *ast.CompositeLit:
		if !isByteSlice(c.typeOf(t.Type)) {
			return nil
		}
		buf := make([]byte, len(t.Elts))
		for i := 0; i < len(t.Elts); i++ {
			t := c.typeAndValueOf(t.Elts[i])
			val, _ := constant.Int64Val(t.Value)
			buf[i] = byte(val)
		}
		return buf
	case *ast.CallExpr:
		if tv := c.typeAndValueOf(t.Args[0]); tv.Value != nil {
			val := constant.StringVal(tv.Value)
			return []byte(val)
		}

		return nil
	default:
		return nil
	}
}

func (c *codegen) convertSyscall(expr *ast.CallExpr, api, name string) {
	syscall, ok := syscalls[api][name]
	if !ok {
		c.prog.Err = fmt.Errorf("unknown VM syscall api: %s.%s", api, name)
		return
	}
	emit.Syscall(c.prog.BinWriter, syscall.API)
	if syscall.ConvertResultToStruct {
		c.emitConvert(stackitem.StructT)
	}

	// This NOP instruction is basically not needed, but if we do, we have a
	// one to one matching avm file with neo-python which is very nice for debugging.
	emit.Opcode(c.prog.BinWriter, opcode.NOP)
}

func (c *codegen) convertBuiltin(expr *ast.CallExpr) {
	var name string
	switch t := expr.Fun.(type) {
	case *ast.Ident:
		name = t.Name
	case *ast.SelectorExpr:
		name = t.Sel.Name
	}

	switch name {
	case "len":
		emit.Opcode(c.prog.BinWriter, opcode.DUP)
		emit.Opcode(c.prog.BinWriter, opcode.ISNULL)
		emit.Instruction(c.prog.BinWriter, opcode.JMPIF, []byte{2 + 1 + 2})
		emit.Opcode(c.prog.BinWriter, opcode.SIZE)
		emit.Instruction(c.prog.BinWriter, opcode.JMP, []byte{2 + 1 + 1})
		emit.Opcode(c.prog.BinWriter, opcode.DROP)
		emit.Opcode(c.prog.BinWriter, opcode.PUSH0)
	case "append":
		arg := expr.Args[0]
		typ := c.typeInfo.Types[arg].Type
		c.emitReverse(len(expr.Args))
		emit.Opcode(c.prog.BinWriter, opcode.DUP)
		emit.Opcode(c.prog.BinWriter, opcode.ISNULL)
		emit.Instruction(c.prog.BinWriter, opcode.JMPIFNOT, []byte{2 + 3})
		if isByteSlice(typ) {
			emit.Opcode(c.prog.BinWriter, opcode.DROP)
			emit.Opcode(c.prog.BinWriter, opcode.PUSH0)
			emit.Opcode(c.prog.BinWriter, opcode.NEWBUFFER)
		} else {
			emit.Opcode(c.prog.BinWriter, opcode.DROP)
			emit.Opcode(c.prog.BinWriter, opcode.NEWARRAY0)
			emit.Opcode(c.prog.BinWriter, opcode.NOP)
		}
		// Jump target.
		for range expr.Args[1:] {
			if isByteSlice(typ) {
				emit.Opcode(c.prog.BinWriter, opcode.SWAP)
				emit.Opcode(c.prog.BinWriter, opcode.CAT)
			} else {
				emit.Opcode(c.prog.BinWriter, opcode.DUP)
				emit.Opcode(c.prog.BinWriter, opcode.ROT)
				emit.Opcode(c.prog.BinWriter, opcode.APPEND)
			}
		}
	case "panic":
		arg := expr.Args[0]
		if isExprNil(arg) {
			emit.Opcode(c.prog.BinWriter, opcode.DROP)
			emit.Opcode(c.prog.BinWriter, opcode.THROW)
		} else if isString(c.typeInfo.Types[arg].Type) {
			ast.Walk(c, arg)
			emit.Syscall(c.prog.BinWriter, interopnames.SystemRuntimeLog)
			emit.Opcode(c.prog.BinWriter, opcode.THROW)
		} else {
			c.prog.Err = errors.New("panic should have string or nil argument")
		}
	case "ToInteger", "ToByteArray", "ToBool":
		typ := stackitem.IntegerT
		switch name {
		case "ToByteArray":
			typ = stackitem.ByteArrayT
		case "ToBool":
			typ = stackitem.BooleanT
		}
		c.emitConvert(typ)
	case "Equals":
		emit.Opcode(c.prog.BinWriter, opcode.EQUAL)
	case "FromAddress":
		// We can be sure that this is a ast.BasicLit just containing a simple
		// address string. Note that the string returned from calling Value will
		// contain double quotes that need to be stripped.
		addressStr := expr.Args[0].(*ast.BasicLit).Value
		addressStr = strings.Replace(addressStr, "\"", "", 2)
		uint160, err := address.StringToUint160(addressStr)
		if err != nil {
			c.prog.Err = err
			return
		}
		bytes := uint160.BytesBE()
		emit.Bytes(c.prog.BinWriter, bytes)
		c.emitConvert(stackitem.BufferT)
	}
}

// transformArgs returns a list of function arguments
// which should be put on stack.
// There are special cases for builtins:
// 1. With FromAddress, parameter conversion is happening at compile-time
//    so there is no need to push parameters on stack and perform an actual call
// 2. With panic, generated code depends on if argument was nil or a string so
//    it should be handled accordingly.
func transformArgs(fun ast.Expr, args []ast.Expr) []ast.Expr {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		if f.Sel.Name == "FromAddress" {
			return args[1:]
		}
	case *ast.Ident:
		if f.Name == "panic" {
			return args[1:]
		}
	}

	return args
}

// emitConvert converts top stack item to the specified type.
func (c *codegen) emitConvert(typ stackitem.Type) {
	emit.Instruction(c.prog.BinWriter, opcode.CONVERT, []byte{byte(typ)})
}

func (c *codegen) convertByteArray(lit *ast.CompositeLit) {
	buf := make([]byte, len(lit.Elts))
	for i := 0; i < len(lit.Elts); i++ {
		t := c.typeAndValueOf(lit.Elts[i])
		val, _ := constant.Int64Val(t.Value)
		buf[i] = byte(val)
	}
	emit.Bytes(c.prog.BinWriter, buf)
	c.emitConvert(stackitem.BufferT)
}

func (c *codegen) convertMap(lit *ast.CompositeLit) {
	emit.Opcode(c.prog.BinWriter, opcode.NEWMAP)
	for i := range lit.Elts {
		elem := lit.Elts[i].(*ast.KeyValueExpr)
		emit.Opcode(c.prog.BinWriter, opcode.DUP)
		ast.Walk(c, elem.Key)
		ast.Walk(c, elem.Value)
		emit.Opcode(c.prog.BinWriter, opcode.SETITEM)
	}
}

func (c *codegen) getStruct(typ types.Type) (*types.Struct, bool) {
	switch t := typ.Underlying().(type) {
	case *types.Struct:
		return t, true
	case *types.Pointer:
		strct, ok := t.Elem().Underlying().(*types.Struct)
		return strct, ok
	default:
		return nil, false
	}
}

func (c *codegen) convertStruct(lit *ast.CompositeLit, ptr bool) {
	// Create a new structScope to initialize and store
	// the positions of its variables.
	strct, ok := c.typeOf(lit).Underlying().(*types.Struct)
	if !ok {
		c.prog.Err = fmt.Errorf("the given literal is not of type struct: %v", lit)
		return
	}

	emit.Opcode(c.prog.BinWriter, opcode.NOP)
	emit.Int(c.prog.BinWriter, int64(strct.NumFields()))
	if ptr {
		emit.Opcode(c.prog.BinWriter, opcode.NEWARRAY)
	} else {
		emit.Opcode(c.prog.BinWriter, opcode.NEWSTRUCT)
	}

	keyedLit := len(lit.Elts) > 0
	if keyedLit {
		_, ok := lit.Elts[0].(*ast.KeyValueExpr)
		keyedLit = keyedLit && ok
	}
	// We need to locally store all the fields, even if they are not initialized.
	// We will initialize all fields to their "zero" value.
	for i := 0; i < strct.NumFields(); i++ {
		sField := strct.Field(i)
		var initialized bool

		emit.Opcode(c.prog.BinWriter, opcode.DUP)
		emit.Int(c.prog.BinWriter, int64(i))

		if !keyedLit {
			if len(lit.Elts) > i {
				ast.Walk(c, lit.Elts[i])
				initialized = true
			}
		} else {
			// Fields initialized by the program.
			for _, field := range lit.Elts {
				f := field.(*ast.KeyValueExpr)
				fieldName := f.Key.(*ast.Ident).Name

				if sField.Name() == fieldName {
					ast.Walk(c, f.Value)
					initialized = true
					break
				}
			}
		}
		if !initialized {
			c.emitDefault(sField.Type())
		}
		emit.Opcode(c.prog.BinWriter, opcode.SETITEM)
	}
}

func (c *codegen) emitToken(tok token.Token, typ types.Type) {
	op, err := convertToken(tok, typ)
	if err != nil {
		c.prog.Err = err
		return
	}
	emit.Opcode(c.prog.BinWriter, op)
}

func convertToken(tok token.Token, typ types.Type) (opcode.Opcode, error) {
	switch tok {
	case token.ADD_ASSIGN, token.ADD:
		// VM has separate opcodes for number and string concatenation
		if isString(typ) {
			return opcode.CAT, nil
		}
		return opcode.ADD, nil
	case token.SUB_ASSIGN:
		return opcode.SUB, nil
	case token.MUL_ASSIGN:
		return opcode.MUL, nil
	case token.QUO_ASSIGN:
		return opcode.DIV, nil
	case token.REM_ASSIGN:
		return opcode.MOD, nil
	case token.SUB:
		return opcode.SUB, nil
	case token.MUL:
		return opcode.MUL, nil
	case token.QUO:
		return opcode.DIV, nil
	case token.REM:
		return opcode.MOD, nil
	case token.LSS:
		return opcode.LT, nil
	case token.LEQ:
		return opcode.LTE, nil
	case token.GTR:
		return opcode.GT, nil
	case token.GEQ:
		return opcode.GTE, nil
	case token.EQL:
		// VM has separate opcodes for number and string equality
		if isNumber(typ) {
			return opcode.NUMEQUAL, nil
		}
		return opcode.EQUAL, nil
	case token.NEQ:
		// VM has separate opcodes for number and string equality
		if isNumber(typ) {
			return opcode.NUMNOTEQUAL, nil
		}
		return opcode.NOTEQUAL, nil
	case token.DEC:
		return opcode.DEC, nil
	case token.INC:
		return opcode.INC, nil
	case token.NOT:
		return opcode.NOT, nil
	case token.AND:
		return opcode.AND, nil
	case token.OR:
		return opcode.OR, nil
	case token.SHL:
		return opcode.SHL, nil
	case token.SHR:
		return opcode.SHR, nil
	case token.XOR:
		return opcode.XOR, nil
	default:
		return 0, fmt.Errorf("compiler could not convert token: %s", tok)
	}
}

func (c *codegen) newFunc(decl *ast.FuncDecl) *funcScope {
	f := c.newFuncScope(decl, c.newLabel())
	c.funcs[c.getFuncNameFromDecl("", decl)] = f
	return f
}

// getFuncNameFromSelector returns fully-qualified function name from the selector expression.
// Second return value is true iff this was a method call, not foreign package call.
func (c *codegen) getFuncNameFromSelector(e *ast.SelectorExpr) (string, bool) {
	ident := e.X.(*ast.Ident)
	if c.typeInfo.Selections[e] != nil {
		typ := c.typeInfo.Types[ident].Type.String()
		return c.getIdentName(typ, e.Sel.Name), true
	}
	return c.getIdentName(ident.Name, e.Sel.Name), false
}

func (c *codegen) newLambda(u uint16, lit *ast.FuncLit) {
	name := fmt.Sprintf("lambda@%d", u)
	f := c.newFuncScope(&ast.FuncDecl{
		Name: ast.NewIdent(name),
		Type: lit.Type,
		Body: lit.Body,
	}, u)
	c.lambda[c.getFuncNameFromDecl("", f.decl)] = f
}

func (c *codegen) compile(info *buildInfo, pkg *loader.PackageInfo) error {
	c.mainPkg = pkg
	c.analyzePkgOrder()
	c.fillDocumentInfo()
	funUsage := c.analyzeFuncUsage()

	// Bring all imported functions into scope.
	c.ForEachFile(c.resolveFuncDecls)

	n, hasInit := c.traverseGlobals()
	if n > 0 || hasInit {
		emit.Opcode(c.prog.BinWriter, opcode.RET)
		c.initEndOffset = c.prog.Len()
	}

	// sort map keys to generate code deterministically.
	keys := make([]*types.Package, 0, len(info.program.AllPackages))
	for p := range info.program.AllPackages {
		keys = append(keys, p)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Path() < keys[j].Path() })

	// Generate the code for the program.
	c.ForEachFile(func(f *ast.File, pkg *types.Package) {
		for _, decl := range f.Decls {
			switch n := decl.(type) {
			case *ast.FuncDecl:
				// Don't convert the function if it's not used. This will save a lot
				// of bytecode space.
				name := c.getFuncNameFromDecl(pkg.Path(), n)
				if !isInitFunc(n) && funUsage.funcUsed(name) && !isInteropPath(pkg.Path()) {
					c.convertFuncDecl(f, n, pkg)
				}
			}
		}
	})

	return c.prog.Err
}

func newCodegen(info *buildInfo, pkg *loader.PackageInfo) *codegen {
	return &codegen{
		buildInfo: info,
		prog:      io.NewBufBinWriter(),
		l:         []int{},
		funcs:     map[string]*funcScope{},
		lambda:    map[string]*funcScope{},
		globals:   map[string]int{},
		labels:    map[labelWithType]uint16{},
		typeInfo:  &pkg.Info,
		constMap:  map[string]types.TypeAndValue{},
		docIndex:  map[string]int{},

		sequencePoints: make(map[string][]DebugSeqPoint),
	}
}

// CodeGen compiles the program to bytecode.
func CodeGen(info *buildInfo) ([]byte, *DebugInfo, error) {
	pkg := info.program.Package(info.initialPackage)
	c := newCodegen(info, pkg)

	if err := c.compile(info, pkg); err != nil {
		return nil, nil, err
	}

	buf, err := c.writeJumps(c.prog.Bytes())
	if err != nil {
		return nil, nil, err
	}
	return buf, c.emitDebugInfo(buf), nil
}

func (c *codegen) resolveFuncDecls(f *ast.File, pkg *types.Package) {
	for _, decl := range f.Decls {
		switch n := decl.(type) {
		case *ast.FuncDecl:
			c.newFunc(n)
		}
	}
}

func (c *codegen) writeJumps(b []byte) ([]byte, error) {
	ctx := vm.NewContext(b)
	var offsets []int
	for op, _, err := ctx.Next(); err == nil && ctx.IP() < len(b); op, _, err = ctx.Next() {
		switch op {
		case opcode.JMP, opcode.JMPIFNOT, opcode.JMPIF, opcode.CALL,
			opcode.JMPEQ, opcode.JMPNE,
			opcode.JMPGT, opcode.JMPGE, opcode.JMPLE, opcode.JMPLT:
		case opcode.JMPL, opcode.JMPIFL, opcode.JMPIFNOTL,
			opcode.JMPEQL, opcode.JMPNEL,
			opcode.JMPGTL, opcode.JMPGEL, opcode.JMPLEL, opcode.JMPLTL,
			opcode.CALLL, opcode.PUSHA:
			// we can't use arg returned by ctx.Next() because it is copied
			nextIP := ctx.NextIP()
			arg := b[nextIP-4:]

			index := binary.LittleEndian.Uint16(arg)
			if int(index) > len(c.l) {
				return nil, fmt.Errorf("unexpected label number: %d (max %d)", index, len(c.l))
			}
			offset := c.l[index] - nextIP + 5
			if offset > math.MaxInt32 || offset < math.MinInt32 {
				return nil, fmt.Errorf("label offset is too big at the instruction %d: %d (max %d, min %d)",
					nextIP-5, offset, math.MaxInt32, math.MinInt32)
			}
			if op != opcode.PUSHA && math.MinInt8 <= offset && offset <= math.MaxInt8 {
				offsets = append(offsets, ctx.IP())
			}
			binary.LittleEndian.PutUint32(arg, uint32(offset))
		}
	}
	// Correct function ip range.
	// Note: indices are sorted in increasing order.
	for _, f := range c.funcs {
	loop:
		for _, ind := range offsets {
			switch {
			case ind > int(f.rng.End):
				break loop
			case ind < int(f.rng.Start):
				f.rng.Start -= longToShortRemoveCount
				f.rng.End -= longToShortRemoveCount
			case ind >= int(f.rng.Start):
				f.rng.End -= longToShortRemoveCount
			}
		}
	}
	return shortenJumps(b, offsets), nil
}

// longToShortRemoveCount is a difference between short and long instruction sizes in bytes.
const longToShortRemoveCount = 3

// shortenJumps returns converts b to a program where all long JMP*/CALL* specified by absolute offsets,
// are replaced with their corresponding short counterparts. It panics if either b or offsets are invalid.
// This is done in 2 passes:
// 1. Alter jump offsets taking into account parts to be removed.
// 2. Perform actual removal of jump targets.
// Note: after jump offsets altering, there can appear new candidates for conversion.
// These are ignored for now.
func shortenJumps(b []byte, offsets []int) []byte {
	if len(offsets) == 0 {
		return b
	}

	// 1. Alter existing jump offsets.
	ctx := vm.NewContext(b)
	for op, _, err := ctx.Next(); err == nil && ctx.IP() < len(b); op, _, err = ctx.Next() {
		// we can't use arg returned by ctx.Next() because it is copied
		nextIP := ctx.NextIP()
		ip := ctx.IP()
		switch op {
		case opcode.JMP, opcode.JMPIFNOT, opcode.JMPIF, opcode.CALL,
			opcode.JMPEQ, opcode.JMPNE,
			opcode.JMPGT, opcode.JMPGE, opcode.JMPLE, opcode.JMPLT:
			offset := int(int8(b[nextIP-1]))
			offset += calcOffsetCorrection(ip, ip+offset, offsets)
			b[nextIP-1] = byte(offset)
		case opcode.JMPL, opcode.JMPIFL, opcode.JMPIFNOTL,
			opcode.JMPEQL, opcode.JMPNEL,
			opcode.JMPGTL, opcode.JMPGEL, opcode.JMPLEL, opcode.JMPLTL,
			opcode.CALLL, opcode.PUSHA:
			arg := b[nextIP-4:]
			offset := int(int32(binary.LittleEndian.Uint32(arg)))
			offset += calcOffsetCorrection(ip, ip+offset, offsets)
			binary.LittleEndian.PutUint32(arg, uint32(offset))
		}
	}

	// 2. Convert instructions.
	copyOffset := 0
	l := len(offsets)
	b[offsets[0]] = byte(toShortForm(opcode.Opcode(b[offsets[0]])))
	for i := 0; i < l; i++ {
		start := offsets[i] + 2
		end := len(b)
		if i != l-1 {
			end = offsets[i+1] + 2
			b[offsets[i+1]] = byte(toShortForm(opcode.Opcode(b[offsets[i+1]])))
		}
		copy(b[start-copyOffset:], b[start+3:end])
		copyOffset += longToShortRemoveCount
	}
	return b[:len(b)-copyOffset]
}

func calcOffsetCorrection(ip, target int, offsets []int) int {
	cnt := 0
	start := sort.Search(len(offsets), func(i int) bool {
		return offsets[i] >= ip || offsets[i] >= target
	})
	for i := start; i < len(offsets) && (offsets[i] < target || offsets[i] <= ip); i++ {
		ind := offsets[i]
		if ip <= ind && ind < target ||
			ind != ip && target <= ind && ind <= ip {
			cnt += longToShortRemoveCount
		}
	}
	if ip < target {
		return -cnt
	}
	return cnt
}

func negateJmp(op opcode.Opcode) opcode.Opcode {
	switch op {
	case opcode.JMPIFL:
		return opcode.JMPIFNOTL
	case opcode.JMPIFNOTL:
		return opcode.JMPIFL
	case opcode.JMPEQL:
		return opcode.JMPNEL
	case opcode.JMPNEL:
		return opcode.JMPEQL
	case opcode.JMPGTL:
		return opcode.JMPLEL
	case opcode.JMPGEL:
		return opcode.JMPLTL
	case opcode.JMPLEL:
		return opcode.JMPGTL
	case opcode.JMPLTL:
		return opcode.JMPGEL
	default:
		panic(fmt.Errorf("invalid opcode in negateJmp: %s", op))
	}
}

func toShortForm(op opcode.Opcode) opcode.Opcode {
	switch op {
	case opcode.JMPL:
		return opcode.JMP
	case opcode.JMPIFL:
		return opcode.JMPIF
	case opcode.JMPIFNOTL:
		return opcode.JMPIFNOT
	case opcode.JMPEQL:
		return opcode.JMPEQ
	case opcode.JMPNEL:
		return opcode.JMPNE
	case opcode.JMPGTL:
		return opcode.JMPGT
	case opcode.JMPGEL:
		return opcode.JMPGE
	case opcode.JMPLEL:
		return opcode.JMPLE
	case opcode.JMPLTL:
		return opcode.JMPLT
	case opcode.CALLL:
		return opcode.CALL
	default:
		panic(fmt.Errorf("invalid opcode: %s", op))
	}
}
