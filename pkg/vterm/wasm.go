// Package vterm provides Go bindings to libvterm via WASM.
package vterm

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed vterm.wasm.zst
var vtermWasmZst []byte

// TermProp represents terminal property types.
type TermProp int

// Terminal property constants (from vterm.h VTermProp enum)
const (
	PropCursorVisible TermProp = 1
	PropCursorBlink   TermProp = 2
	PropAltScreen     TermProp = 3
	PropTitle         TermProp = 4
	PropIconName      TermProp = 5
	PropReverse       TermProp = 6
	PropCursorShape   TermProp = 7
	PropMouse         TermProp = 8
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

// VTerm represents a libvterm terminal instance.
type VTerm struct {
	ctx    context.Context
	mod    api.Module
	mu     sync.Mutex
	closed bool

	// WASM pointers
	vtPtr     uint32 // VTerm*
	screenPtr uint32 // VTermScreen*
	statePtr  uint32 // VTermState*

	rows, cols int
	handle     int32 // Handle for callbacks

	// Callbacks
	onDamage     func(startRow, endRow, startCol, endCol int)
	onMoveCursor func(row, col int, visible bool)
	onBell       func()
	onPushLine   func(line string)
	onTermProp   func(prop TermProp, val any)

	// Exported functions (cached for performance)
	fnNew             api.Function
	fnFree            api.Function
	fnSetSize         api.Function
	fnGetSize         api.Function
	fnInputWrite      api.Function
	fnObtainScreen    api.Function
	fnObtainState     api.Function
	fnScreenReset     api.Function
	fnScreenGetCell   api.Function
	fnScreenGetText   api.Function
	fnStateGetCursor  api.Function
	fnScreenEnableAlt api.Function
	fnSetCallbacks    api.Function

	// Memory management
	fnMalloc api.Function
	fnFreeM  api.Function
}

// Global runtime - reused across all VTerm instances
var (
	wasmRuntime     wazero.Runtime
	wasmModule      api.Module
	wasmRuntimeOnce sync.Once
	wasmRuntimeErr  error

	// Handle registry for callbacks
	handleRegistry = make(map[int32]*VTerm)
	handleMu       sync.RWMutex
	nextHandle     int32 = 1

	// Struct layout info (populated once at init)
	cellLayout struct {
		size           uint32
		charsOff       uint32
		widthOff       uint32
		attrsOff       uint32
		fgOff          uint32
		bgOff          uint32
		colorTypeMask  uint8
		colorIndexed   uint8
		colorDefaultFg uint8
		colorDefaultBg uint8
	}
)

func registerHandle(vt *VTerm) int32 {
	handleMu.Lock()
	defer handleMu.Unlock()
	h := nextHandle
	nextHandle++
	handleRegistry[h] = vt
	vt.handle = h
	return h
}

func unregisterHandle(h int32) {
	handleMu.Lock()
	defer handleMu.Unlock()
	delete(handleRegistry, h)
}

func getVTermByHandle(h int32) *VTerm {
	handleMu.RLock()
	defer handleMu.RUnlock()
	return handleRegistry[h]
}

func initWasmRuntime(ctx context.Context) error {
	wasmRuntimeOnce.Do(func() {
		wasmRuntime = wazero.NewRuntime(ctx)

		// Register host functions for callbacks
		_, wasmRuntimeErr = wasmRuntime.NewHostModuleBuilder("env").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, handle, startRow, endRow, startCol, endCol int32) {
				if vt := getVTermByHandle(handle); vt != nil && vt.onDamage != nil {
					vt.onDamage(int(startRow), int(endRow), int(startCol), int(endCol))
				}
			}).
			Export("host_damage").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, handle, row, col, visible int32) {
				if vt := getVTermByHandle(handle); vt != nil && vt.onMoveCursor != nil {
					vt.onMoveCursor(int(row), int(col), visible != 0)
				}
			}).
			Export("host_movecursor").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, m api.Module, handle, prop, value, strPtr, strLen int32) {
				vt := getVTermByHandle(handle)
				if vt == nil || vt.onTermProp == nil {
					return
				}
				goProp := TermProp(prop)
				var goVal any
				switch goProp {
				case PropTitle, PropIconName:
					if strPtr != 0 && strLen > 0 {
						data, ok := m.Memory().Read(uint32(strPtr), uint32(strLen))
						if ok {
							goVal = string(data)
						}
					} else {
						goVal = ""
					}
				case PropCursorVisible, PropCursorBlink, PropAltScreen, PropReverse:
					goVal = value != 0
				case PropCursorShape, PropMouse:
					goVal = int(value)
				default:
					return
				}
				vt.onTermProp(goProp, goVal)
			}).
			Export("host_settermprop").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, handle int32) {
				if vt := getVTermByHandle(handle); vt != nil && vt.onBell != nil {
					vt.onBell()
				}
			}).
			Export("host_bell").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, m api.Module, handle, cellsPtr, cols int32) {
				vt := getVTermByHandle(handle)
				if vt == nil || vt.onPushLine == nil {
					return
				}
				if cellsPtr == 0 || cols <= 0 {
					return
				}

				mem := m.Memory()
				var result []byte
				var lastAttrs CellAttrs
				var lastFg, lastBg Color
				firstCell := true
				emittedANSI := false

				for i := range cols {
					cellPtr := uint32(cellsPtr) + uint32(i)*cellLayout.size
					cell := vt.cellFromMemory(mem, cellPtr)

					// Skip wide char padding (0xFFFFFFFF as int32 is -1)
					if len(cell.Chars) == 1 && cell.Chars[0] == -1 {
						continue
					}

					// Emit ANSI codes if attributes changed
					if firstCell || cell.Attrs != lastAttrs || cell.Fg != lastFg || cell.Bg != lastBg {
						ansi := vt.ansiForCell(cell, lastAttrs, lastFg, lastBg, firstCell)
						if len(ansi) > 0 {
							result = append(result, ansi...)
							emittedANSI = true
						}
						lastAttrs = cell.Attrs
						lastFg = cell.Fg
						lastBg = cell.Bg
						firstCell = false
					}

					// Output character
					if len(cell.Chars) > 0 {
						for _, r := range cell.Chars {
							result = append(result, runeToUTF8(r)...)
						}
					} else {
						result = append(result, ' ')
					}
				}

				// Reset attributes at end
				if emittedANSI {
					result = append(result, "\x1b[0m"...)
				}

				vt.onPushLine(string(result))
			}).
			Export("host_pushline_cells").
			Instantiate(ctx)
		if wasmRuntimeErr != nil {
			return
		}

		// Instantiate WASI
		wasi_snapshot_preview1.MustInstantiate(ctx, wasmRuntime)

		// Decompress and instantiate the vterm module
		dec, err := zstd.NewReader(bytes.NewReader(vtermWasmZst))
		if err != nil {
			wasmRuntimeErr = fmt.Errorf("zstd reader: %w", err)
			return
		}
		vtermWasm, err := dec.DecodeAll(vtermWasmZst, nil)
		dec.Close()
		if err != nil {
			wasmRuntimeErr = fmt.Errorf("zstd decompress: %w", err)
			return
		}

		wasmModule, wasmRuntimeErr = wasmRuntime.Instantiate(ctx, vtermWasm)
		if wasmRuntimeErr != nil {
			return
		}

		// Get struct layout info
		if fn := wasmModule.ExportedFunction("sizeof_cell"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.size = uint32(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("offsetof_cell_chars"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.charsOff = uint32(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("offsetof_cell_width"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.widthOff = uint32(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("offsetof_cell_attrs"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.attrsOff = uint32(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("offsetof_cell_fg"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.fgOff = uint32(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("offsetof_cell_bg"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.bgOff = uint32(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("color_type_mask"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.colorTypeMask = uint8(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("color_indexed"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.colorIndexed = uint8(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("color_default_fg"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.colorDefaultFg = uint8(results[0])
			}
		}
		if fn := wasmModule.ExportedFunction("color_default_bg"); fn != nil {
			if results, err := fn.Call(ctx); err == nil {
				cellLayout.colorDefaultBg = uint8(results[0])
			}
		}
	})
	return wasmRuntimeErr
}

// New creates a new VTerm instance using WASM.
func New(rows, cols int) (*VTerm, error) {
	ctx := context.Background()

	if err := initWasmRuntime(ctx); err != nil {
		return nil, fmt.Errorf("failed to init wasm runtime: %w", err)
	}

	vt := &VTerm{
		ctx:  ctx,
		mod:  wasmModule,
		rows: rows,
		cols: cols,
	}

	// Cache function references
	vt.fnNew = wasmModule.ExportedFunction("vterm_new")
	vt.fnFree = wasmModule.ExportedFunction("vterm_free")
	vt.fnSetSize = wasmModule.ExportedFunction("vterm_set_size")
	vt.fnGetSize = wasmModule.ExportedFunction("vterm_get_size")
	vt.fnInputWrite = wasmModule.ExportedFunction("vterm_input_write")
	vt.fnObtainScreen = wasmModule.ExportedFunction("vterm_obtain_screen")
	vt.fnObtainState = wasmModule.ExportedFunction("vterm_obtain_state")
	vt.fnScreenReset = wasmModule.ExportedFunction("vterm_screen_reset")
	vt.fnScreenGetCell = wasmModule.ExportedFunction("vterm_screen_get_cell")
	vt.fnScreenGetText = wasmModule.ExportedFunction("vterm_screen_get_text")
	vt.fnStateGetCursor = wasmModule.ExportedFunction("vterm_state_get_cursorpos")
	vt.fnScreenEnableAlt = wasmModule.ExportedFunction("vterm_screen_enable_altscreen")
	vt.fnSetCallbacks = wasmModule.ExportedFunction("wasm_set_callbacks")
	vt.fnMalloc = wasmModule.ExportedFunction("malloc")
	vt.fnFreeM = wasmModule.ExportedFunction("free")

	// Create vterm instance
	results, err := vt.fnNew.Call(ctx, uint64(rows), uint64(cols))
	if err != nil {
		return nil, fmt.Errorf("vterm_new failed: %w", err)
	}
	vt.vtPtr = uint32(results[0])
	if vt.vtPtr == 0 {
		return nil, fmt.Errorf("vterm_new returned NULL")
	}

	// Set UTF-8 mode
	fnSetUtf8 := wasmModule.ExportedFunction("vterm_set_utf8")
	if _, err := fnSetUtf8.Call(ctx, uint64(vt.vtPtr), 1); err != nil {
		return nil, fmt.Errorf("vterm_set_utf8 failed: %w", err)
	}

	// Get screen
	results, err = vt.fnObtainScreen.Call(ctx, uint64(vt.vtPtr))
	if err != nil {
		return nil, fmt.Errorf("vterm_obtain_screen failed: %w", err)
	}
	vt.screenPtr = uint32(results[0])

	// Reset screen
	if _, err := vt.fnScreenReset.Call(ctx, uint64(vt.screenPtr), 1); err != nil {
		return nil, fmt.Errorf("vterm_screen_reset failed: %w", err)
	}

	// Enable altscreen
	if _, err := vt.fnScreenEnableAlt.Call(ctx, uint64(vt.screenPtr), 1); err != nil {
		return nil, fmt.Errorf("vterm_screen_enable_altscreen failed: %w", err)
	}

	// Get state for cursor queries
	results, err = vt.fnObtainState.Call(ctx, uint64(vt.vtPtr))
	if err != nil {
		return nil, fmt.Errorf("vterm_obtain_state failed: %w", err)
	}
	vt.statePtr = uint32(results[0])

	// Register handle and set up callbacks
	handle := registerHandle(vt)
	if _, err := vt.fnSetCallbacks.Call(ctx, uint64(vt.screenPtr), uint64(handle)); err != nil {
		unregisterHandle(handle)
		return nil, fmt.Errorf("wasm_set_callbacks failed: %w", err)
	}

	return vt, nil
}

// Free releases the VTerm resources.
func (v *VTerm) Free() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed {
		return
	}
	v.closed = true

	// Unregister handle
	if v.handle != 0 {
		unregisterHandle(v.handle)
		v.handle = 0
	}

	if v.vtPtr != 0 {
		v.fnFree.Call(v.ctx, uint64(v.vtPtr))
		v.vtPtr = 0
	}
}

// SetSize resizes the terminal.
func (v *VTerm) SetSize(rows, cols int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.fnSetSize.Call(v.ctx, uint64(v.vtPtr), uint64(rows), uint64(cols))
	v.rows = rows
	v.cols = cols
}

// GetSize returns the current terminal size.
func (v *VTerm) GetSize() (rows, cols int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.rows, v.cols
}

// alloc allocates memory in the WASM module.
func (v *VTerm) alloc(size uint32) (uint32, error) {
	results, err := v.fnMalloc.Call(v.ctx, uint64(size))
	if err != nil {
		return 0, err
	}
	ptr := uint32(results[0])
	if ptr == 0 {
		return 0, fmt.Errorf("malloc returned NULL")
	}
	return ptr, nil
}

// free releases memory in the WASM module.
func (v *VTerm) free(ptr uint32) {
	v.fnFreeM.Call(v.ctx, uint64(ptr))
}

// Write feeds input bytes to the terminal.
func (v *VTerm) Write(data []byte) int {
	if len(data) == 0 {
		return 0
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	// Allocate buffer in WASM memory
	bufPtr, err := v.alloc(uint32(len(data)))
	if err != nil {
		return 0
	}
	defer v.free(bufPtr)

	// Write data to allocated buffer
	mem := v.mod.Memory()
	if !mem.Write(bufPtr, data) {
		return 0
	}

	results, err := v.fnInputWrite.Call(v.ctx, uint64(v.vtPtr), uint64(bufPtr), uint64(len(data)))
	if err != nil {
		return 0
	}

	return int(results[0])
}

// GetCursor returns the current cursor position.
func (v *VTerm) GetCursor() (row, col int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// VTermPos is {int row, int col} = 8 bytes
	posPtr, err := v.alloc(8)
	if err != nil {
		return 0, 0
	}
	defer v.free(posPtr)

	_, err = v.fnStateGetCursor.Call(v.ctx, uint64(v.statePtr), uint64(posPtr))
	if err != nil {
		return 0, 0
	}

	// Read VTermPos struct
	mem := v.mod.Memory()
	rowVal, _ := mem.ReadUint32Le(posPtr)
	colVal, _ := mem.ReadUint32Le(posPtr + 4)

	return int(int32(rowVal)), int(int32(colVal))
}

// GetRowText returns the text content of a row.
func (v *VTerm) GetRowText(row int) string {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.getRowTextLocked(row)
}

func (v *VTerm) getRowTextLocked(row int) string {
	bufLen := uint32(v.cols * 4) // UTF-8 can be up to 4 bytes per char

	// Allocate buffer for text output
	bufPtr, err := v.alloc(bufLen)
	if err != nil {
		return ""
	}
	defer v.free(bufPtr)

	// VTermRect: {start_row, end_row, start_col, end_col} = 16 bytes
	rectPtr, err := v.alloc(16)
	if err != nil {
		return ""
	}
	defer v.free(rectPtr)

	mem := v.mod.Memory()

	// Write rect
	mem.WriteUint32Le(rectPtr, uint32(row))
	mem.WriteUint32Le(rectPtr+4, uint32(row+1))
	mem.WriteUint32Le(rectPtr+8, 0)
	mem.WriteUint32Le(rectPtr+12, uint32(v.cols))

	results, err := v.fnScreenGetText.Call(v.ctx, uint64(v.screenPtr), uint64(bufPtr), uint64(bufLen), uint64(rectPtr))
	if err != nil {
		return ""
	}

	textLen := int(results[0])
	if textLen == 0 {
		return ""
	}

	textBytes, ok := mem.Read(bufPtr, uint32(textLen))
	if !ok {
		return ""
	}

	// Trim trailing spaces
	text := string(textBytes)
	for len(text) > 0 && text[len(text)-1] == ' ' {
		text = text[:len(text)-1]
	}

	return text
}

// GetScreenText returns the full screen content.
func (v *VTerm) GetScreenText() string {
	v.mu.Lock()
	defer v.mu.Unlock()

	var result strings.Builder
	for row := 0; row < v.rows; row++ {
		if row > 0 {
			result.WriteString("\n")
		}
		result.WriteString(v.getRowTextLocked(row))
	}
	return result.String()
}

// GetCell returns the cell at the given position.
func (v *VTerm) GetCell(row, col int) Cell {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.getCellLocked(row, col)
}

func (v *VTerm) getCellLocked(row, col int) Cell {
	// VTermPos: {row, col} = 8 bytes
	posPtr, err := v.alloc(8)
	if err != nil {
		return Cell{}
	}
	defer v.free(posPtr)

	mem := v.mod.Memory()
	mem.WriteUint32Le(posPtr, uint32(row))
	mem.WriteUint32Le(posPtr+4, uint32(col))

	// Allocate VTermScreenCell using the actual size from C
	cellPtr, err := v.alloc(cellLayout.size)
	if err != nil {
		return Cell{}
	}
	defer v.free(cellPtr)

	_, err = v.fnScreenGetCell.Call(v.ctx, uint64(v.screenPtr), uint64(posPtr), uint64(cellPtr))
	if err != nil {
		return Cell{}
	}

	return v.cellFromMemory(mem, cellPtr)
}

// cellFromMemory reads a VTermScreenCell from WASM memory using layout offsets.
func (v *VTerm) cellFromMemory(mem api.Memory, ptr uint32) Cell {
	c := Cell{}

	// Read chars (6 x uint32) at charsOff
	charsPtr := ptr + cellLayout.charsOff
	for i := range 6 {
		charVal, _ := mem.ReadUint32Le(charsPtr + uint32(i*4))
		if charVal == 0 {
			break
		}
		if charVal != 0xFFFFFFFF && charVal != 0 { // Skip wide char padding marker and null
			c.Chars = append(c.Chars, rune(charVal))
		}
	}

	// Read width
	widthByte, _ := mem.ReadByte(ptr + cellLayout.widthOff)
	c.Width = int(int8(widthByte))

	// Read attrs (bitfield)
	attrsVal, _ := mem.ReadUint16Le(ptr + cellLayout.attrsOff)
	c.Attrs = CellAttrs{
		Bold:      attrsVal&(1<<0) != 0,
		Underline: int((attrsVal >> 1) & 0x3),
		Italic:    attrsVal&(1<<3) != 0,
		Blink:     attrsVal&(1<<4) != 0,
		Reverse:   attrsVal&(1<<5) != 0,
		Conceal:   attrsVal&(1<<6) != 0,
		Strike:    attrsVal&(1<<7) != 0,
	}

	// Read fg color (4 bytes: type, r/idx, g, b)
	c.Fg = v.colorFromMemory(mem, ptr+cellLayout.fgOff)

	// Read bg color (4 bytes: type, r/idx, g, b)
	c.Bg = v.colorFromMemory(mem, ptr+cellLayout.bgOff)

	return c
}

// colorFromMemory reads a VTermColor from WASM memory.
// VTermColor is a 4-byte union: type (1 byte) + rgb/indexed data (3 bytes)
func (v *VTerm) colorFromMemory(mem api.Memory, ptr uint32) Color {
	colorType, _ := mem.ReadByte(ptr)
	byte1, _ := mem.ReadByte(ptr + 1)
	byte2, _ := mem.ReadByte(ptr + 2)
	byte3, _ := mem.ReadByte(ptr + 3)

	col := Color{
		DefaultFg: colorType&cellLayout.colorDefaultFg != 0,
		DefaultBg: colorType&cellLayout.colorDefaultBg != 0,
	}

	if colorType&cellLayout.colorTypeMask == cellLayout.colorIndexed {
		col.Type = ColorIndexed
		col.Index = byte1
	} else {
		col.Type = ColorRGB
		col.R = byte1
		col.G = byte2
		col.B = byte3
	}

	return col
}

// RenderRowANSI returns a single row with ANSI escape sequences.
func (v *VTerm) RenderRowANSI(row int) string {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.renderRowANSILocked(row)
}

func (v *VTerm) renderRowANSILocked(row int) string {
	var result []byte
	var lastAttrs CellAttrs
	var lastFg, lastBg Color
	firstCell := true
	emittedANSI := false

	for col := 0; col < v.cols; col++ {
		cell := v.getCellLocked(row, col)

		// Skip wide char padding
		if len(cell.Chars) == 1 && cell.Chars[0] == rune(-1) {
			continue
		}

		// Emit ANSI codes if attributes changed
		if firstCell || cell.Attrs != lastAttrs || cell.Fg != lastFg || cell.Bg != lastBg {
			ansi := v.ansiForCell(cell, lastAttrs, lastFg, lastBg, firstCell)
			if len(ansi) > 0 {
				result = append(result, ansi...)
				emittedANSI = true
			}
			lastAttrs = cell.Attrs
			lastFg = cell.Fg
			lastBg = cell.Bg
			firstCell = false
		}

		// Output character
		if len(cell.Chars) > 0 {
			for _, r := range cell.Chars {
				result = append(result, runeToUTF8(r)...)
			}
		} else {
			result = append(result, ' ')
		}
	}

	// Reset attributes at end
	if emittedANSI {
		result = append(result, "\x1b[0m"...)
	}

	return string(result)
}

// ansiForCell generates ANSI escape sequences (simplified version).
func (v *VTerm) ansiForCell(cell Cell, lastAttrs CellAttrs, lastFg, lastBg Color, first bool) []byte {
	var codes []int

	hasAttrs := cell.Attrs.Bold || cell.Attrs.Italic || cell.Attrs.Underline > 0 ||
		cell.Attrs.Reverse || cell.Attrs.Strike || !cell.Fg.DefaultFg || !cell.Bg.DefaultBg

	needReset := false
	if !first {
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
		if !lastFg.DefaultFg && cell.Fg.DefaultFg {
			needReset = true
		}
		if !lastBg.DefaultBg && cell.Bg.DefaultBg {
			needReset = true
		}
	}

	if needReset || (first && hasAttrs) {
		codes = append(codes, 0)
	}

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

	result := []byte("\x1b[")
	for i, code := range codes {
		if i > 0 {
			result = append(result, ';')
		}
		result = append(result, fmt.Sprintf("%d", code)...)
	}
	result = append(result, 'm')
	return result
}

// GetScreenANSI returns the screen content with ANSI escape sequences.
func (v *VTerm) GetScreenANSI() string {
	v.mu.Lock()
	defer v.mu.Unlock()

	var lines []string
	for row := 0; row < v.rows; row++ {
		lines = append(lines, v.renderRowANSILocked(row))
	}

	var result strings.Builder
	for i, line := range lines {
		if i > 0 {
			result.WriteString("\r\n")
		}
		result.WriteString(line)
	}
	return result.String()
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

// runeToUTF8 converts a rune to UTF-8 bytes.
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
