// Package vterm provides Go bindings to libvterm, a terminal emulator library.
package vterm

/*
#cgo CFLAGS: -I${SRCDIR}/libvterm/include -I${SRCDIR}/libvterm/src
#include <vterm.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>

// Forward declarations for callbacks
extern int goScreenDamage(VTermRect rect, void* user);
extern int goScreenMoveCursor(VTermPos pos, VTermPos oldpos, int visible, void* user);
extern int goScreenSetTermProp(VTermProp prop, VTermValue* val, void* user);
extern int goScreenBell(void* user);
extern int goScreenPushLine(int cols, const VTermScreenCell* cells, void* user);

static VTermScreenCallbacks go_screen_callbacks = {
    .damage = goScreenDamage,
    .moverect = NULL,
    .movecursor = goScreenMoveCursor,
    .settermprop = goScreenSetTermProp,
    .bell = goScreenBell,
    .resize = NULL,
    .sb_pushline = goScreenPushLine,
    .sb_popline = NULL,
    .sb_clear = NULL,
};

static void set_screen_callbacks(VTermScreen* screen, uintptr_t handle) {
    vterm_screen_set_callbacks(screen, &go_screen_callbacks, (void*)handle);
}

// Helper to extract string from VTermValue for VTERM_PROP_TITLE
static const char* get_string_value(VTermValue* val, size_t* len) {
    *len = val->string.len;
    return val->string.str;
}

static int get_boolean_value(VTermValue* val) {
    return val->boolean;
}

// Color helpers - need these because VTermColor is a union
static int color_is_indexed(VTermColor* c) {
    return VTERM_COLOR_IS_INDEXED(c);
}

static int color_is_default_fg(VTermColor* c) {
    return VTERM_COLOR_IS_DEFAULT_FG(c);
}

static int color_is_default_bg(VTermColor* c) {
    return VTERM_COLOR_IS_DEFAULT_BG(c);
}

static void get_rgb(VTermColor* c, uint8_t* r, uint8_t* g, uint8_t* b) {
    *r = c->rgb.red;
    *g = c->rgb.green;
    *b = c->rgb.blue;
}

static uint8_t get_indexed(VTermColor* c) {
    return c->indexed.idx;
}

// Cell attribute helpers - cgo can't access bitfields
static int cell_attr_bold(VTermScreenCell* cell) { return cell->attrs.bold; }
static int cell_attr_underline(VTermScreenCell* cell) { return cell->attrs.underline; }
static int cell_attr_italic(VTermScreenCell* cell) { return cell->attrs.italic; }
static int cell_attr_blink(VTermScreenCell* cell) { return cell->attrs.blink; }
static int cell_attr_reverse(VTermScreenCell* cell) { return cell->attrs.reverse; }
static int cell_attr_conceal(VTermScreenCell* cell) { return cell->attrs.conceal; }
static int cell_attr_strike(VTermScreenCell* cell) { return cell->attrs.strike; }
*/
import "C"
import (
	"fmt"
	"runtime/cgo"
	"strings"
	"sync"
	"unsafe"
)

// VTerm represents a libvterm terminal instance.
type VTerm struct {
	vt     *C.VTerm
	screen *C.VTermScreen
	mu     sync.Mutex

	// Callbacks
	onDamage     func(startRow, endRow, startCol, endCol int)
	onMoveCursor func(row, col int, visible bool)
	onBell       func()
	onPushLine   func(line string)
	onTermProp   func(prop TermProp, val any)

	// Handle for cgo callback
	handle cgo.Handle
}

// TermProp represents terminal property types.
type TermProp int

const (
	PropCursorVisible TermProp = C.VTERM_PROP_CURSORVISIBLE
	PropCursorBlink   TermProp = C.VTERM_PROP_CURSORBLINK
	PropAltScreen     TermProp = C.VTERM_PROP_ALTSCREEN
	PropTitle         TermProp = C.VTERM_PROP_TITLE
	PropIconName      TermProp = C.VTERM_PROP_ICONNAME
	PropReverse       TermProp = C.VTERM_PROP_REVERSE
	PropCursorShape   TermProp = C.VTERM_PROP_CURSORSHAPE
	PropMouse         TermProp = C.VTERM_PROP_MOUSE
)

// Color represents a terminal color.
type Color struct {
	Type      ColorType
	R, G, B   uint8
	Index     uint8
	DefaultFg bool
	DefaultBg bool
}

// ColorType indicates whether a color is RGB or indexed.
type ColorType int

const (
	ColorRGB     ColorType = 0
	ColorIndexed ColorType = 1
)

// CellAttrs represents cell attributes.
type CellAttrs struct {
	Bold      bool
	Underline int // 0=off, 1=single, 2=double, 3=curly
	Italic    bool
	Blink     bool
	Reverse   bool
	Conceal   bool
	Strike    bool
}

// Cell represents a screen cell.
type Cell struct {
	Chars []rune
	Width int
	Attrs CellAttrs
	Fg    Color
	Bg    Color
}

// New creates a new VTerm instance with the given dimensions.
func New(rows, cols int) *VTerm {
	vt := &VTerm{
		vt: C.vterm_new(C.int(rows), C.int(cols)),
	}
	C.vterm_set_utf8(vt.vt, 1)

	vt.screen = C.vterm_obtain_screen(vt.vt)
	C.vterm_screen_reset(vt.screen, 1)
	C.vterm_screen_enable_altscreen(vt.screen, 1)

	// Create handle and set callbacks
	vt.handle = cgo.NewHandle(vt)
	C.set_screen_callbacks(vt.screen, C.uintptr_t(vt.handle))

	return vt
}

// Free releases the VTerm resources.
func (v *VTerm) Free() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vt != nil {
		v.handle.Delete()
		C.vterm_free(v.vt)
		v.vt = nil
	}
}

// SetSize resizes the terminal.
func (v *VTerm) SetSize(rows, cols int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	C.vterm_set_size(v.vt, C.int(rows), C.int(cols))
}

// GetSize returns the current terminal size.
func (v *VTerm) GetSize() (rows, cols int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var r, c C.int
	C.vterm_get_size(v.vt, &r, &c)
	return int(r), int(c)
}

// Write feeds input bytes to the terminal.
func (v *VTerm) Write(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	n := C.vterm_input_write(v.vt, (*C.char)(unsafe.Pointer(&data[0])), C.size_t(len(data)))
	return int(n)
}

// GetCell returns the cell at the given position.
func (v *VTerm) GetCell(row, col int) Cell {
	v.mu.Lock()
	defer v.mu.Unlock()

	pos := C.VTermPos{row: C.int(row), col: C.int(col)}
	var cell C.VTermScreenCell
	C.vterm_screen_get_cell(v.screen, pos, &cell)

	return v.cellFromC(&cell)
}

// GetRowText returns the text content of a row.
func (v *VTerm) GetRowText(row int) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.getRowTextUnsafe(row)
}

// getRowTextUnsafe returns the text content of a row without locking.
func (v *VTerm) getRowTextUnsafe(row int) string {
	rows, cols := v.getSizeUnsafe()
	if row < 0 || row >= rows {
		return ""
	}

	var result []byte
	pos := C.VTermPos{row: C.int(row)}

	for col := 0; col < cols; col++ {
		pos.col = C.int(col)
		var cell C.VTermScreenCell
		C.vterm_screen_get_cell(v.screen, pos, &cell)

		// Skip padding cells for wide characters (marked with -1 / 0xFFFFFFFF)
		if cell.chars[0] == 0xFFFFFFFF {
			continue
		}

		// Convert UTF-32 chars to UTF-8
		hasChar := false
		for i := 0; i < C.VTERM_MAX_CHARS_PER_CELL && cell.chars[i] != 0; i++ {
			result = append(result, runeToUTF8(rune(cell.chars[i]))...)
			hasChar = true
		}
		if !hasChar {
			result = append(result, ' ')
		}
	}

	// Trim trailing spaces
	for len(result) > 0 && result[len(result)-1] == ' ' {
		result = result[:len(result)-1]
	}
	return string(result)
}

// GetScreenText returns the full screen content.
func (v *VTerm) GetScreenText() string {
	v.mu.Lock()
	defer v.mu.Unlock()

	rows, _ := v.getSizeUnsafe()
	var result string
	for row := 0; row < rows; row++ {
		if row > 0 {
			result += "\n"
		}
		result += v.getRowTextUnsafe(row)
	}
	return result
}

// GetScreenANSI returns the screen content with ANSI escape sequences for colors/attributes.
func (v *VTerm) GetScreenANSI() string {
	v.mu.Lock()
	defer v.mu.Unlock()

	rows, cols := v.getSizeUnsafe()
	var lines []string

	for row := 0; row < rows; row++ {
		pos := C.VTermPos{row: C.int(row)}
		line := v.cellsToANSI(cols, func(col int) *C.VTermScreenCell {
			pos.col = C.int(col)
			var cell C.VTermScreenCell
			C.vterm_screen_get_cell(v.screen, pos, &cell)
			return &cell
		})
		lines = append(lines, line)
	}

	return strings.Join(lines, "\r\n")
}

// RenderRowANSI returns a single row with ANSI escape sequences for colors/attributes.
func (v *VTerm) RenderRowANSI(row int) string {
	v.mu.Lock()
	defer v.mu.Unlock()

	_, cols := v.getSizeUnsafe()
	pos := C.VTermPos{row: C.int(row)}
	return v.cellsToANSI(cols, func(col int) *C.VTermScreenCell {
		pos.col = C.int(col)
		var cell C.VTermScreenCell
		C.vterm_screen_get_cell(v.screen, pos, &cell)
		return &cell
	})
}

// cellsToANSI converts a line of cells to an ANSI-formatted string.
// getCell returns the cell at position i (0 to count-1).
func (v *VTerm) cellsToANSI(count int, getCell func(i int) *C.VTermScreenCell) string {
	var result []byte
	var lastAttrs CellAttrs
	var lastFg, lastBg Color
	firstCell := true
	emittedANSI := false

	// Output all cells (no trimming - we need to overwrite the full row)
	for i := 0; i < count; i++ {
		cell := getCell(i)

		// Skip padding cells for wide characters
		if cell.chars[0] == 0xFFFFFFFF {
			continue
		}

		cellData := v.cellFromC(cell)

		// Emit ANSI codes if attributes changed
		if firstCell || cellData.Attrs != lastAttrs || cellData.Fg != lastFg || cellData.Bg != lastBg {
			ansi := v.ansiForCell(cellData, lastAttrs, lastFg, lastBg, firstCell)
			if len(ansi) > 0 {
				result = append(result, ansi...)
				emittedANSI = true
			}
			lastAttrs = cellData.Attrs
			lastFg = cellData.Fg
			lastBg = cellData.Bg
			firstCell = false
		}

		// Output character
		if len(cellData.Chars) > 0 {
			for _, r := range cellData.Chars {
				result = append(result, runeToUTF8(r)...)
			}
		} else {
			result = append(result, ' ')
		}
	}

	// Reset attributes at end if we emitted any ANSI codes
	if emittedANSI {
		result = append(result, "\x1b[0m"...)
	}

	return string(result)
}

// ansiForCell generates ANSI escape sequences for cell attribute changes.
func (v *VTerm) ansiForCell(cell Cell, lastAttrs CellAttrs, lastFg, lastBg Color, first bool) []byte {
	var codes []int

	// Check if cell has any non-default attributes
	hasAttrs := cell.Attrs.Bold || cell.Attrs.Italic || cell.Attrs.Underline > 0 ||
		cell.Attrs.Reverse || cell.Attrs.Strike || !cell.Fg.DefaultFg || !cell.Bg.DefaultBg

	// Check if we need a reset first
	needReset := false
	if !first {
		// If turning off an attribute, we need reset
		if lastAttrs.Bold && !cell.Attrs.Bold {
			needReset = true
		}
		if lastAttrs.Italic && !cell.Attrs.Italic {
			needReset = true
		}
		if lastAttrs.Underline > 0 && cell.Attrs.Underline == 0 {
			needReset = true
		}
		if lastAttrs.Reverse && !cell.Attrs.Reverse {
			needReset = true
		}
		// If going back to default colors from non-default, we need reset
		if !lastFg.DefaultFg && cell.Fg.DefaultFg {
			needReset = true
		}
		if !lastBg.DefaultBg && cell.Bg.DefaultBg {
			needReset = true
		}
	}

	// Only emit reset if needed, not for first cell with default attrs
	if needReset || (first && hasAttrs) {
		codes = append(codes, 0)
	}

	// Add attribute codes
	if cell.Attrs.Bold {
		codes = append(codes, 1)
	}
	if cell.Attrs.Italic {
		codes = append(codes, 3)
	}
	if cell.Attrs.Underline == 1 {
		codes = append(codes, 4)
	}
	if cell.Attrs.Reverse {
		codes = append(codes, 7)
	}
	if cell.Attrs.Strike {
		codes = append(codes, 9)
	}

	// Foreground color
	if !cell.Fg.DefaultFg {
		if cell.Fg.Type == ColorIndexed {
			if cell.Fg.Index < 8 {
				codes = append(codes, 30+int(cell.Fg.Index))
			} else if cell.Fg.Index < 16 {
				codes = append(codes, 90+int(cell.Fg.Index)-8)
			} else {
				codes = append(codes, 38, 5, int(cell.Fg.Index))
			}
		} else {
			codes = append(codes, 38, 2, int(cell.Fg.R), int(cell.Fg.G), int(cell.Fg.B))
		}
	}

	// Background color
	if !cell.Bg.DefaultBg {
		if cell.Bg.Type == ColorIndexed {
			if cell.Bg.Index < 8 {
				codes = append(codes, 40+int(cell.Bg.Index))
			} else if cell.Bg.Index < 16 {
				codes = append(codes, 100+int(cell.Bg.Index)-8)
			} else {
				codes = append(codes, 48, 5, int(cell.Bg.Index))
			}
		} else {
			codes = append(codes, 48, 2, int(cell.Bg.R), int(cell.Bg.G), int(cell.Bg.B))
		}
	}

	if len(codes) == 0 {
		return nil
	}

	// Build escape sequence
	result := []byte("\x1b[")
	for i, code := range codes {
		if i > 0 {
			result = append(result, ';')
		}
		result = append(result, []byte(fmt.Sprintf("%d", code))...)
	}
	result = append(result, 'm')
	return result
}

// GetCursor returns the current cursor position.
func (v *VTerm) GetCursor() (row, col int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	state := C.vterm_obtain_state(v.vt)
	var pos C.VTermPos
	C.vterm_state_get_cursorpos(state, &pos)
	return int(pos.row), int(pos.col)
}

// ConvertColorToRGB converts an indexed color to RGB.
func (v *VTerm) ConvertColorToRGB(c *Color) {
	if c.Type != ColorIndexed {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	var vc C.VTermColor
	C.vterm_color_indexed(&vc, C.uint8_t(c.Index))
	C.vterm_screen_convert_color_to_rgb(v.screen, &vc)

	var r, g, b C.uint8_t
	C.get_rgb(&vc, &r, &g, &b)
	c.R, c.G, c.B = uint8(r), uint8(g), uint8(b)
	c.Type = ColorRGB
}

// GetDefaultColors returns the default foreground and background colors.
func (v *VTerm) GetDefaultColors() (fg, bg Color) {
	v.mu.Lock()
	defer v.mu.Unlock()

	state := C.vterm_obtain_state(v.vt)
	var fgC, bgC C.VTermColor
	C.vterm_state_get_default_colors(state, &fgC, &bgC)

	return v.colorFromC(&fgC), v.colorFromC(&bgC)
}

// Callback setters

// OnDamage sets the callback for screen damage events.
func (v *VTerm) OnDamage(fn func(startRow, endRow, startCol, endCol int)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onDamage = fn
}

// OnMoveCursor sets the callback for cursor movement.
func (v *VTerm) OnMoveCursor(fn func(row, col int, visible bool)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onMoveCursor = fn
}

// OnBell sets the callback for bell events.
func (v *VTerm) OnBell(fn func()) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onBell = fn
}

// OnPushLine sets the callback for scrollback pushes.
func (v *VTerm) OnPushLine(fn func(line string)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onPushLine = fn
}

// OnTermProp sets the callback for terminal property changes.
func (v *VTerm) OnTermProp(fn func(prop TermProp, val any)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onTermProp = fn
}

// Internal helpers

func (v *VTerm) getSizeUnsafe() (rows, cols int) {
	var r, c C.int
	C.vterm_get_size(v.vt, &r, &c)
	return int(r), int(c)
}

func (v *VTerm) cellFromC(cell *C.VTermScreenCell) Cell {
	c := Cell{
		Width: int(cell.width),
		Attrs: CellAttrs{
			Bold:      C.cell_attr_bold(cell) != 0,
			Underline: int(C.cell_attr_underline(cell)),
			Italic:    C.cell_attr_italic(cell) != 0,
			Blink:     C.cell_attr_blink(cell) != 0,
			Reverse:   C.cell_attr_reverse(cell) != 0,
			Conceal:   C.cell_attr_conceal(cell) != 0,
			Strike:    C.cell_attr_strike(cell) != 0,
		},
		Fg: v.colorFromC(&cell.fg),
		Bg: v.colorFromC(&cell.bg),
	}

	for i := 0; i < C.VTERM_MAX_CHARS_PER_CELL && cell.chars[i] != 0; i++ {
		c.Chars = append(c.Chars, rune(cell.chars[i]))
	}

	return c
}

func (v *VTerm) colorFromC(c *C.VTermColor) Color {
	col := Color{
		DefaultFg: C.color_is_default_fg(c) != 0,
		DefaultBg: C.color_is_default_bg(c) != 0,
	}

	if C.color_is_indexed(c) != 0 {
		col.Type = ColorIndexed
		col.Index = uint8(C.get_indexed(c))
	} else {
		col.Type = ColorRGB
		var r, g, b C.uint8_t
		C.get_rgb(c, &r, &g, &b)
		col.R, col.G, col.B = uint8(r), uint8(g), uint8(b)
	}

	return col
}

func runeToUTF8(r rune) []byte {
	if r < 0x80 {
		return []byte{byte(r)}
	} else if r < 0x800 {
		return []byte{
			byte(0xC0 | (r >> 6)),
			byte(0x80 | (r & 0x3F)),
		}
	} else if r < 0x10000 {
		return []byte{
			byte(0xE0 | (r >> 12)),
			byte(0x80 | ((r >> 6) & 0x3F)),
			byte(0x80 | (r & 0x3F)),
		}
	} else {
		return []byte{
			byte(0xF0 | (r >> 18)),
			byte(0x80 | ((r >> 12) & 0x3F)),
			byte(0x80 | ((r >> 6) & 0x3F)),
			byte(0x80 | (r & 0x3F)),
		}
	}
}
