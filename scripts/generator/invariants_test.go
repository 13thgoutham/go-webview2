package generator

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These are properties of the WHOLE generated binding, checked against the pinned IDL, rather than
// goldens for one interface. Each corresponds to a defect family that shipped for months, and the
// reason none was caught by review is the same in every case: the wrong output is valid Go that
// compiles, links and returns S_OK. A golden file records what one interface looked like on the day
// it was written; only a property says what must be true of all 300 of them.
//
// They also fail loudly if a template change is right for the interface a golden covers and wrong
// elsewhere -- which is the shape of every hand-patched fix in this package's history.

// latestIDL is the version pinned in latest_version.txt.
const latestIDL = "WebView2.1.0.2903.40.idl"

func generateFromPinnedIDL(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", latestIDL))
	require.NoError(t, err)
	files, err := ParseIDL(data)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.FileName] = f.Content.String()
	}
	return out
}

// interfaceBases returns each interface's declared base, straight from the IDL. The IDL is the only
// authority on this: the version-suffixed names invite inferring the chain from the numbering, and
// ICoreWebView2EnvironmentOptions2 through 8 are exactly where that inference is wrong -- each of
// those derives from IUnknown, not from its predecessor.
func interfaceBases(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", latestIDL))
	require.NoError(t, err)
	idl, err := Parser.ParseBytes("", data)
	require.NoError(t, err)
	require.NoError(t, idl.Process())

	bases := map[string]string{}
	for _, lib := range idl.Libraries {
		for _, d := range lib.Declarations {
			if d.Interface != nil {
				bases[d.Interface.Name] = d.Interface.BaseClass
			}
		}
	}
	require.NotEmpty(t, bases)
	return bases
}

// TestVtableEmbedsDeclaredBase is the regression test for the worst defect in this package's
// history, and it is a property rather than a slot-offset assertion on purpose.
//
// A COM vtable is flat: a derived interface's vtable begins with its ENTIRE base chain, then its own
// methods. Every derived vtable was generated as IUnknownVtbl plus its own methods, so each method
// sat too early by however many methods the chain above it declares, and a call landed on whichever
// unrelated function occupies that offset. ICoreWebView2_14's AddServerCertificateErrorDetected
// dispatched at slot 4 -- ICoreWebView2::get_Settings, which has one out-parameter and therefore
// wrote the Settings pointer over the caller's event-handler struct -- instead of slot 107. It
// returned S_OK, so registration "succeeded" and the event never fired. 88 interfaces were affected.
//
// Asserting embedding rather than computed offsets is deliberate: embedding the immediate base is
// sufficient by induction, and a test that recomputes the offsets would be asserting its own
// arithmetic against itself.
func TestVtableEmbedsDeclaredBase(t *testing.T) {
	files := generateFromPinnedIDL(t)
	bases := interfaceBases(t)

	checked := 0
	for name, base := range bases {
		content, ok := files[name+".go"]
		if !ok {
			continue // forward declaration only
		}
		want := base + "Vtbl"
		if base == "IUnknown" {
			want = "IUnknownVtbl"
		}
		// The embedded field is the first line of the vtable struct.
		decl := name + "Vtbl struct {\n\t"
		idx := strings.Index(content, decl)
		require.NotEqual(t, -1, idx, "%s has no vtable struct", name)
		rest := content[idx+len(decl):]
		got := rest[:strings.IndexByte(rest, '\n')]
		require.Equal(t, want, got,
			"%sVtbl must embed %s: the IDL declares %s : %s, and a vtable that omits an "+
				"intermediate interface's slots shifts every later method to the wrong offset",
			name, want, name, base)
		checked++
	}
	require.Greater(t, checked, 250, "expected the pinned IDL to yield hundreds of interfaces")
}

// TestQueryInterfaceAccessorReceiver checks the other half of the same mistake, with the rule that
// actually applies: QueryInterface asks an OBJECT for another of its interfaces, so the accessor
// belongs on an interface of the object that can answer -- which the declared chain's ROOT names,
// not the immediate base.
//
// Every accessor was emitted on ICoreWebView2. For the ICoreWebView2_N chain that is correct, since
// it is all one object. For every other chain it is useless: GetICoreWebView2Controller2 hung off
// ICoreWebView2, a different object, so it could only fail, while ICoreWebView2Controller had no
// accessor at all. 56 accessors were misrooted; the 26 on ICoreWebView2 stay put, so no existing
// caller breaks.
//
// Interfaces whose base is IUnknown keep the ICoreWebView2 receiver they ship with -- there is no
// sibling interface to reach them from, and re-rooting them onto IUnknown would move ~170 methods
// onto it and delete accessors that already ship. That is an API break, not a bug fix.
func TestQueryInterfaceAccessorReceiver(t *testing.T) {
	files := generateFromPinnedIDL(t)
	bases := interfaceBases(t)

	// root walks the declared chain to the ancestor whose own base is IUnknown.
	root := func(name string) string {
		out := ""
		for {
			base, ok := bases[name]
			if !ok || base == "IUnknown" {
				return out
			}
			out, name = base, base
		}
	}

	checked, rerooted := 0, 0
	for name, base := range bases {
		content, ok := files[name+".go"]
		if !ok {
			continue
		}
		if !strings.Contains(content, fmt.Sprintf(") Get%s() *%s {", name, name)) {
			continue // no accessor is generated for a chain root
		}
		receiver := root(name)
		if receiver == "" {
			receiver = "ICoreWebView2" // base is IUnknown; see above
		}
		require.Contains(t, content,
			fmt.Sprintf("func (i *%s) Get%s() *%s {", receiver, name, name),
			"Get%s must be a method on %s, the object a caller can QueryInterface from", name, receiver)
		if receiver != "ICoreWebView2" {
			rerooted++
		}
		checked++
		_ = base
	}
	require.GreaterOrEqual(t, checked, 80, "expected many version-suffixed accessors")
	require.GreaterOrEqual(t, rerooted, 50,
		"expected the Controller/Environment/Profile/Settings/Frame chains to root on their own object")
}

var invokeSignature = regexp.MustCompile(`(?m)^func \w+Invoke\(this \*\w+(?:, ([^)]*))?\) uintptr \{`)

// TestCallbackParamsFitInAUintptr is the regression test for the family PR #36 patched in the
// output. A callback reached through syscall.NewCallback may not declare a parameter wider than a
// uintptr, and NewCallback checks this at CALLBACK CONSTRUCTION time -- which happens in a
// package-level var initialiser, so a violation panics during package init for any program that
// merely imports pkg/webview2, used or not:
//
//	panic: compileCallback: argument size is larger than uintptr
//
// A Go string is a 16-byte header, and three CompletedHandlers declared their LPCWSTR result as one.
// That makes this property the difference between the package being importable and not, which is
// also why it is worth a test rather than trusting three goldens.
func TestCallbackParamsFitInAUintptr(t *testing.T) {
	files := generateFromPinnedIDL(t)

	checked := 0
	for fileName, content := range files {
		for _, m := range invokeSignature.FindAllStringSubmatch(content, -1) {
			if m[1] == "" {
				continue
			}
			for _, param := range strings.Split(m[1], ",") {
				fields := strings.Fields(strings.TrimSpace(param))
				require.Len(t, fields, 2, "unexpected parameter %q in %s", param, fileName)
				typ := fields[1]
				require.NotEqual(t, "string", typ,
					"%s: callback parameter %q is a Go string (16 bytes); LPCWSTR arrives as a "+
						"pointer, so it must be declared *uint16 or syscall.NewCallback rejects "+
						"the whole vtable at init", fileName, fields[0])
				require.False(t, strings.HasPrefix(typ, "[]") || strings.Contains(typ, "interface{"),
					"%s: callback parameter %q has type %s, which is wider than a uintptr",
					fileName, fields[0], typ)
			}
			checked++
		}
	}
	require.Greater(t, checked, 50, "expected the pinned IDL to yield many handler Invoke functions")
}

// TestCallErrnoIsNeverReturnedAsError guards the fix that upstream applied to the generated output
// twice by hand and never to the template. ComProc.Call's third result is a syscall.Errno, which is
// non-nil on SUCCESS ("The operation completed successfully"), so binding it to err and returning it
// made every successful call look like a failure. HRESULT is the real status.
func TestCallErrnoIsNeverReturnedAsError(t *testing.T) {
	files := generateFromPinnedIDL(t)

	for fileName, content := range files {
		if fileName == "com.go" {
			// com.tmpl's hand-written IStream.Read does bind err, and correctly: it compares
			// against windows.ERROR_SUCCESS, which IS Errno(0), rather than against nil.
			continue
		}
		require.NotContains(t, content, ", _, err := i.Vtbl.",
			"%s binds ComProc.Call's Errno, which is non-nil on success", fileName)
	}
}

// TestByValueArgumentsAreNotPassedByAddress is table-driven over synthetic IDL rather than a
// property over the real one, because the wrong and right forms are distinguishable only if you know
// the parameter's type -- which the generated text alone does not tell you.
//
// The Windows x64 rule being pinned: an aggregate of exactly 1, 2, 4 or 8 bytes is passed IN A
// REGISTER as an integer of that width, anything else by address. So POINT (8 bytes) and RECT (16)
// take opposite forms, which is how a single &address default came to look plausible.
func TestByValueArgumentsAreNotPassedByAddress(t *testing.T) {
	cases := []struct {
		name  string
		param string
		want  string
	}{
		{"BOOL in-param", "[in] BOOL value", "boolToUintptr(value),"},
		{"UINT32 in-param", "[in] UINT32 value", "uintptr(value),"},
		{"HWND is a uintptr typedef", "[in] HWND value", "uintptr(value),"},
		{"8-byte token goes in a register", "[in] EventRegistrationToken value",
			"uintptr(*(*uint64)(unsafe.Pointer(&value))),"},
		{"8-byte POINT goes in a register", "[in] POINT value",
			"uintptr(*(*uint64)(unsafe.Pointer(&value))),"},
		{"4-byte COLOR goes in a register", "[in] COREWEBVIEW2_COLOR value",
			"uintptr(*(*uint32)(unsafe.Pointer(&value))),"},
		{"16-byte RECT is too wide, so by address", "[in] RECT value",
			"uintptr(unsafe.Pointer(&value)),"},
		{"in-param pointer is already the address", "[in] ICoreWebView2Settings* value",
			"uintptr(unsafe.Pointer(value)),"},
		{"out-param needs the address of our storage", "[out, retval] UINT32* value",
			"uintptr(unsafe.Pointer(&value)),"},
		{"out-param string needs the address of our pointer", "[out, retval] LPWSTR* value",
			"uintptr(unsafe.Pointer(&_value)),"},
		{"in-param string is already a *uint16", "[in] LPCWSTR value",
			"uintptr(unsafe.Pointer(_value)),"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idl := fmt.Sprintf(`
[uuid(26d34152-879f-4065-bea2-3daa2cfadfb8), version(1.0)]
library WebView2 {
[uuid(A0D6DF20-3B92-416D-AA0C-437A9C727857), object, pointer_default(unique)]
interface ICoreWebView2Probe : IUnknown {
  HRESULT Probe(%s);
}
}`, c.param)

			files, err := ParseIDL([]byte(idl))
			require.NoError(t, err)

			var content string
			for _, f := range files {
				if f.FileName == "ICoreWebView2Probe.go" {
					content = f.Content.String()
				}
			}
			require.NotEmpty(t, content, "probe interface was not generated")
			require.Contains(t, content, c.want,
				"wrong marshalling for %q.\ngenerated:\n%s", c.param, content)
		})
	}
}

// TestGeneratedOutputIsFormatted asserts the generator's output is a gofmt fixpoint. It was not,
// and the committed tree was gofmt-clean anyway, meaning someone reformatted 306 files after each
// regeneration. The cost was ~180 files differing from a fresh generation by import order and blank
// lines alone -- enough noise to hide a real change in a regeneration diff, which is how
// hand-patched output came to survive here in the first place.
func TestGeneratedOutputIsFormatted(t *testing.T) {
	for fileName, content := range generateFromPinnedIDL(t) {
		formatted, err := format.Source([]byte(content))
		require.NoError(t, err, "%s is not valid Go", fileName)
		require.Equal(t, string(formatted), content,
			"%s is not gofmt-clean as generated", fileName)
	}
}
