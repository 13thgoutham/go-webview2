package types

import (
	"fmt"
	"io"
	"log"
	"strings"
)

type Param struct {
	Direction *Direction `parser:"@@?"`
	Type      string     `parser:"@Ident"`
	Const     string     `parser:"@('const')?"`
	Pointer   string     `parser:"@('*')*"`
	Name      string     `parser:"@Ident ','?"`

	// Processed
	GoType       string
	InputGoType  string
	OutputGoType string

	// This is used to generate setup code for the Go inputs
	setupTemplate   string
	cleanupTemplate string
	LocalName       string
	decl            *InterfaceMethod
	VtableCallInput string
}

func (p *Param) IsOutputParam() bool {
	if p.Direction == nil {
		return false
	}
	return p.Direction.Dir == "out"
}

func (p *Param) LocalVariableType() string {
	//if p.IsOutputParam() && p.isDoublePointer() {
	//	return p.GoType[1:]
	//}
	return p.GoType
}

func (p *Param) Process(decl *InterfaceMethod) {
	p.decl = decl
	p.GoType = IdlTypeToGoType(p.Type)
	if p.isDoublePointer() {
		p.GoType = "*" + p.GoType
	}
	p.OutputGoType = p.GoType
	if p.IsOutputParam() && strings.HasPrefix(p.OutputGoType, "**") {
		p.OutputGoType = p.GoType[1:]
	}
	p.InputGoType = p.GoType
}

func (p *Param) isPointer() bool {
	return p.Pointer != ""
}

func (p *Param) isSinglePointer() bool {
	return p.Pointer == "*"
}

func (p *Param) isDoublePointer() bool {
	return p.Pointer == "**"
}

func (p *Param) AsInputType() string {
	if p.isPointer() && p.GoType != "string" {
		return "*" + p.GoType
	}
	return p.GoType
}

// AsCallbackType is the type an INBOUND parameter must be declared as, and is deliberately not
// AsInputType. An outbound call is free to take a Go string, because the generated method
// converts it with UTF16PtrFromString before it reaches the vtable. A callback has no such
// step: syscall.NewCallback hands the Go function the raw machine words WebView2 passed, so the
// declared type IS the ABI. LPCWSTR arrives as a pointer, hence *uint16.
//
// Declaring it "string" does not merely mis-marshal; it fails to load at all. Every handler
// vtable is built in a package-level var initialiser, so NewComProc -> syscall.NewCallback runs
// during package init, and NewCallback rejects any argument wider than a uintptr. A string
// header is 16 bytes on amd64, so importing pkg/webview2 panicked before main() -- whether or
// not the program ever used these three handlers:
//
//	panic: compileCallback: argument size is larger than uintptr
//
// BOOL is left as Go's bool (ICoreWebView2PrintToPdf/TrySuspendCompletedHandler) although it is
// the same family. bool is 1 byte against a 4-byte BOOL, so the callee reads the low byte of the
// word -- correct for the 0/1 Win32 BOOL actually carries, and NewCallback accepts it because
// 1 <= sizeof(uintptr). Widening it to int32 would change those two Impl interfaces for every
// caller that implements them, to fix nothing observable.
func (p *Param) AsCallbackType() string {
	if p.GoType == "string" {
		return "*uint16"
	}
	return p.AsInputType()
}

func (p *Param) processSetup() {
	p.processSetupInputs()
	p.processSetupOutputs()
	p.processVtableCallInput()
}

func (p *Param) SetupCode(w io.Writer) {
	if p.setupTemplate == "" {
		return
	}
	data := struct {
		Param       *Param
		ErrorValues string
	}{
		Param:       p,
		ErrorValues: p.decl.ErrorValues(),
	}
	mustTemplate("Param Setup: "+p.setupTemplate, p.setupTemplate, &data, w)
}
func (p *Param) CleanupCode(w io.Writer) {
	if p.cleanupTemplate == "" {
		return
	}
	mustTemplate("Param Cleanup: "+p.cleanupTemplate, p.cleanupTemplate, p, w)
}

func (p *Param) IsInputParam() bool {
	return !p.IsOutputParam()
}

func (p *Param) processVtableCallInput() {
	variableName := p.GetVariableName()

	// This block used to test p.Type -- the IDL type, so "BOOL", "INT32", "double" -- against
	// Go type names in lower case. It therefore never matched, and every by-value input
	// parameter fell through to the &address catch-all at the bottom of this function, which
	// makes the callee read a pointer as an integer. p.GoType is the mapped Go type and is
	// what the comparisons meant. One wrong field name, and no scalar in-parameter in the
	// whole binding was passed correctly.
	if !p.isPointer() {
		switch {
		case p.GoType == "bool":
			// A COM BOOL in-param is a 4-byte integer passed by value. uintptr(someBool) is
			// not a legal Go conversion, hence the helper in com.tmpl.
			p.VtableCallInput = "boolToUintptr(" + variableName + ")"
			return
		case strings.HasPrefix(p.GoType, "int"), strings.HasPrefix(p.GoType, "uint"):
			p.VtableCallInput = "uintptr(" + variableName + ")"
			return
		}
		// Handle typedefs and aggregates. See byValueArgument in maps.go for the ABI rule and for
		// why no single rule covers them: EventRegistrationToken alone is 61 call sites, and every
		// remove_ method in the binding handed the callee the ADDRESS of the 8-byte token it was
		// supposed to match, so no event handler could ever be removed -- and remove_ still
		// returned S_OK.
		if expr, ok := byValueArgument[p.Type]; ok {
			p.VtableCallInput = fmt.Sprintf(expr, variableName)
			return
		}
		if _, ok := byRefAggregate[p.Type]; ok {
			// Wider than a register, so here the address genuinely is the argument.
			p.VtableCallInput = "uintptr(unsafe.Pointer(&" + variableName + "))"
			return
		}
		// float32/float64 are deliberately NOT handled here. On Windows x64 a floating-point
		// argument is passed in XMM0-XMM3, and syscall.LazyProc.Call can only fill the integer
		// registers -- so uintptr(f) would silently truncate and the &f below is equally
		// wrong. There is no marshalling fix; it needs a different call mechanism. Left on the
		// existing path so this change does not alter behaviour it cannot make correct.
		// ICoreWebView2Controller.PutZoomFactor is the live example.
		// Strings and enums are classified further down (the LPCWSTR case and IsEnum), and floats
		// have no correct answer, so those three are exempt rather than unclassified.
		if p.GoType != "string" && !strings.HasPrefix(p.GoType, "float") && !p.IsEnum() {
			// Refuse to guess. Every defect in this family was the &address default landing on a
			// type nobody had classified, and each one returned S_OK while corrupting exactly one
			// argument. A new IDL type should cost one line in maps.go, not a silent wire bug.
			log.Fatalf("generator: by-value in-parameter %q of type %q (%s.%s) is in neither "+
				"byValueArgument nor byRefAggregate in maps.go. Classify it: an aggregate of "+
				"1, 2, 4 or 8 bytes is passed in a register as an integer of that width; "+
				"anything else is passed by address.",
				variableName, p.Type, p.decl.decl.Name, p.decl.ProcessedName)
		}
	}
	switch p.Type {
	case "LPCWSTR", "LPWSTR":
		// Direction matters here and was not being distinguished. An OUT parameter is
		// declared LPWSTR* : the callee writes a string pointer into storage we own, so it
		// needs the ADDRESS of our local *uint16. Passing the local's (nil) value instead
		// gave the callee a null to write through, so every string getter returned empty --
		// silently, since the HRESULT was S_OK. An IN parameter is already the *uint16 that
		// UTF16PtrFromString produced and is passed as-is.
		if p.IsOutputParam() {
			p.VtableCallInput = "uintptr(unsafe.Pointer(&" + variableName + "))"
		} else {
			p.VtableCallInput = "uintptr(unsafe.Pointer(" + variableName + "))"
		}
		return
	}
	if p.Pointer == "**" {
		p.VtableCallInput = "uintptr(unsafe.Pointer(&" + variableName + "))"
		return
	}
	if p.Pointer == "*" {
		if p.IsOutputParam() {
			p.VtableCallInput = "uintptr(unsafe.Pointer(&" + variableName + "))"
		} else {
			p.VtableCallInput = "uintptr(unsafe.Pointer(" + variableName + "))"
		}
		return
	}
	if p.IsEnum() {
		p.VtableCallInput = "uintptr(" + variableName + ")"
		return
	}
	p.VtableCallInput = "uintptr(unsafe.Pointer(&" + variableName + "))"
}

func (p *Param) ClearLocalName() string {
	p.LocalName = ""
	return ""
}

func (p *Param) GetVariableName() string {
	result := p.LocalName
	if result == "" {
		result = p.Name
	}
	return result
}

func (p *Param) GetReturnVariableName() string {
	result := p.LocalName
	if result == "" {
		result = p.Name
	}
	return result
}

func (p *Param) IsEnum() bool {
	return p.decl.decl.decl.library.enums.Contains(p.Type)
}

func (p *Param) processSetupInputs() {
	if !p.IsInputParam() {
		return
	}
	switch p.GoType {
	case "string":
		// We need to convert to *uint16
		p.setupTemplate = "inputStringSetup.tmpl"
		p.LocalName = "_" + p.Name
	}
}

func (p *Param) processSetupOutputs() {
	if !p.IsOutputParam() {
		return
	}
	switch p.GoType {
	case "string":
		p.LocalName = "_" + p.Name
		p.setupTemplate = "outputStringSetup.tmpl"
		p.cleanupTemplate = "outputStringCleanup.tmpl"
	case "bool":
		p.LocalName = "_" + p.Name
		p.setupTemplate = "outputBoolSetup.tmpl"
		p.cleanupTemplate = "outputBoolCleanup.tmpl"
	default:
		p.setupTemplate = "outputDefaultSetup.tmpl"
	}
	if p.Pointer != "" {
		p.decl.decl.includes.AddUnique(`"unsafe"`)
	}
}

func (p *Param) defaultErrorValue() string {

	switch true {
	case p.IsEnum(), strings.HasPrefix(p.GoType, "uint"), strings.HasPrefix(p.GoType, "int"),
		p.GoType == "HANDLE", p.GoType == "HWND", p.GoType == "HCURSOR":
		return "0"
	case strings.HasPrefix(p.GoType, "float"):
		return "0.0"
	case p.GoType == "bool":
		return "false"
	case p.GoType == "string":
		return `""`
	case p.OutputGoType[0] == '*':
		return "nil"
	default:
		return p.GoType + "{}"
	}
}

type Direction struct {
	Dir    string `parser:"'[' @('out'|'in')"`
	Retval string `parser:"(',' @('retval'|'size_is' '(' Ident ')') )? ']'"`
}
