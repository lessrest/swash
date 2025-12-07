package vterm

/*
#include <vterm.h>

// Helper to extract string from VTermValue for VTERM_PROP_TITLE
static const char* get_string_value(VTermValue* val, size_t* len) {
    *len = val->string.len;
    return val->string.str;
}

static int get_boolean_value(VTermValue* val) {
    return val->boolean;
}

static int get_number_value(VTermValue* val) {
    return val->number;
}

// Cell attribute helpers for scrollback callback
static int sb_cell_attr_bold(const VTermScreenCell* cell) { return cell->attrs.bold; }
static int sb_cell_attr_underline(const VTermScreenCell* cell) { return cell->attrs.underline; }
static int sb_cell_attr_italic(const VTermScreenCell* cell) { return cell->attrs.italic; }
static int sb_cell_attr_blink(const VTermScreenCell* cell) { return cell->attrs.blink; }
static int sb_cell_attr_reverse(const VTermScreenCell* cell) { return cell->attrs.reverse; }
static int sb_cell_attr_conceal(const VTermScreenCell* cell) { return cell->attrs.conceal; }
static int sb_cell_attr_strike(const VTermScreenCell* cell) { return cell->attrs.strike; }
*/
import "C"
import (
	"runtime/cgo"
	"unsafe"
)

//export goScreenDamage
func goScreenDamage(rect C.VTermRect, user unsafe.Pointer) C.int {
	h := cgo.Handle(user)
	v := h.Value().(*VTerm)
	if v.onDamage != nil {
		v.onDamage(int(rect.start_row), int(rect.end_row), int(rect.start_col), int(rect.end_col))
	}
	return 0
}

//export goScreenMoveCursor
func goScreenMoveCursor(pos C.VTermPos, oldpos C.VTermPos, visible C.int, user unsafe.Pointer) C.int {
	h := cgo.Handle(user)
	v := h.Value().(*VTerm)
	if v.onMoveCursor != nil {
		v.onMoveCursor(int(pos.row), int(pos.col), visible != 0)
	}
	return 0
}

//export goScreenSetTermProp
func goScreenSetTermProp(prop C.VTermProp, val *C.VTermValue, user unsafe.Pointer) C.int {
	h := cgo.Handle(user)
	v := h.Value().(*VTerm)
	if v.onTermProp == nil {
		return 0
	}

	goProp := TermProp(prop)
	var goVal any

	switch goProp {
	case PropTitle, PropIconName:
		var length C.size_t
		str := C.get_string_value(val, &length)
		if str != nil && length > 0 {
			goVal = C.GoStringN(str, C.int(length))
		} else {
			goVal = ""
		}
	case PropCursorVisible, PropCursorBlink, PropAltScreen, PropReverse:
		goVal = C.get_boolean_value(val) != 0
	case PropCursorShape, PropMouse:
		goVal = int(C.get_number_value(val))
	default:
		return 0
	}

	v.onTermProp(goProp, goVal)
	return 0
}

//export goScreenBell
func goScreenBell(user unsafe.Pointer) C.int {
	h := cgo.Handle(user)
	v := h.Value().(*VTerm)
	if v.onBell != nil {
		v.onBell()
	}
	return 0
}

//export goScreenPushLine
func goScreenPushLine(cols C.int, cells *C.VTermScreenCell, user unsafe.Pointer) C.int {
	h := cgo.Handle(user)
	v := h.Value().(*VTerm)
	if v.onPushLine == nil {
		return 0
	}

	// Use shared helper to convert cells to ANSI string
	line := v.cellsToANSI(int(cols), func(i int) *C.VTermScreenCell {
		return (*C.VTermScreenCell)(unsafe.Pointer(uintptr(unsafe.Pointer(cells)) + uintptr(i)*unsafe.Sizeof(*cells)))
	})

	v.onPushLine(line)
	return 0
}
