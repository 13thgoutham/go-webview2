package types

var idlTypeToGoType = map[string]string{
	"IUnknown":               "IUnknown",
	"EventRegistrationToken": "EventRegistrationToken",
	"LPWSTR":                 "string",
	"LPCWSTR":                "string",
	"HRESULT":                "uintptr",
	"UINT64":                 "uint64",
	"UINT32":                 "uint32",
	"UINT":                   "uint",
	"INT":                    "int",
	"INT32":                  "int32",
	"INT64":                  "int64",
	"BOOL":                   "bool",
	"BYTE":                   "uint8",
	"DWORD":                  "uint32",
	"double":                 "float64",
}

func IdlTypeToGoType(input string) string {
	result := idlTypeToGoType[input]
	if result == "" {
		return input
	}
	return result
}

// byValueArgument gives, per IDL type, the expression that passes a BY-VALUE in-parameter of that
// type to ComProc.Call. %s is the variable name. Only types that are neither a mapped scalar nor
// an enum reach here: the handle typedefs and the aggregates.
//
// The rule being encoded is the Windows x64 calling convention's, and it is not "aggregates go by
// reference". An aggregate of exactly 1, 2, 4 or 8 bytes is passed IN A REGISTER as an integer of
// that width; anything else is passed by reference. So POINT (8) and COREWEBVIEW2_COLOR (4) are
// register-passed while RECT (16) is not, and a single rule cannot cover both -- which is how
// every one of these ended up as &address, the answer that is only ever right for the large ones.
//
// Reinterpreting the struct through a same-width integer copies its layout rather than re-deriving
// the field order by hand, and it is a plain read, so unlike &address it also has no
// unsafe.Pointer lifetime question.
var byValueArgument = map[string]string{
	// type X uintptr in com.tmpl: integers already, so the value IS the argument.
	"HWND":      "uintptr(%s)",
	"HANDLE":    "uintptr(%s)",
	"HBRUSH":    "uintptr(%s)",
	"HCURSOR":   "uintptr(%s)",
	"HICON":     "uintptr(%s)",
	"HINSTANCE": "uintptr(%s)",
	"HMENU":     "uintptr(%s)",
	"HMODULE":   "uintptr(%s)",
	"VARIANT":   "uintptr(%s)",

	// Register-sized aggregates.
	"EventRegistrationToken": "uintptr(*(*uint64)(unsafe.Pointer(&%s)))", // struct{ value int64 }
	"POINT":                  "uintptr(*(*uint64)(unsafe.Pointer(&%s)))", // struct{ X, Y int32 }
	"COREWEBVIEW2_COLOR":     "uintptr(*(*uint32)(unsafe.Pointer(&%s)))", // struct{ A, R, G, B byte }
}

// byRefAggregate are the by-value in-parameter types that really are passed by address, with the
// width that makes them so. They are listed rather than left to a default so that a type in
// NEITHER table is a generator error instead of a silent guess -- the failure mode this entire
// family is made of. Adding a struct to the IDL should cost one line here and a look at its size.
var byRefAggregate = map[string]int{
	"RECT":                             16, // struct{ Left, Top, Right, Bottom int32 }
	"COREWEBVIEW2_PHYSICAL_KEY_STATUS": 24, // 2x UINT32 + 4x BOOL
}
