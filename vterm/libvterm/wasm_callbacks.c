// WASM callback wrapper for libvterm
// These callbacks are registered with vterm and call imported host functions

#include "include/vterm.h"
#include <stdint.h>
#include <stddef.h>

// ============================================================================
// Struct layout exports - called once at init to get field offsets
// ============================================================================

__attribute__((export_name("sizeof_cell")))
int sizeof_cell(void) { return sizeof(VTermScreenCell); }

__attribute__((export_name("offsetof_cell_chars")))
int offsetof_cell_chars(void) { return offsetof(VTermScreenCell, chars); }

__attribute__((export_name("offsetof_cell_width")))
int offsetof_cell_width(void) { return offsetof(VTermScreenCell, width); }

__attribute__((export_name("offsetof_cell_attrs")))
int offsetof_cell_attrs(void) { return offsetof(VTermScreenCell, attrs); }

__attribute__((export_name("offsetof_cell_fg")))
int offsetof_cell_fg(void) { return offsetof(VTermScreenCell, fg); }

__attribute__((export_name("offsetof_cell_bg")))
int offsetof_cell_bg(void) { return offsetof(VTermScreenCell, bg); }

// VTermColor type flags (from vterm.h)
// VTERM_COLOR_INDEXED = 0x01, VTERM_COLOR_DEFAULT_FG = 0x02, VTERM_COLOR_DEFAULT_BG = 0x04
__attribute__((export_name("color_type_mask")))
int color_type_mask(void) { return VTERM_COLOR_TYPE_MASK; }

__attribute__((export_name("color_indexed")))
int color_indexed(void) { return VTERM_COLOR_INDEXED; }

__attribute__((export_name("color_default_fg")))
int color_default_fg(void) { return VTERM_COLOR_DEFAULT_FG; }

__attribute__((export_name("color_default_bg")))
int color_default_bg(void) { return VTERM_COLOR_DEFAULT_BG; }

// Host function imports - these are provided by the Go host via wazero
__attribute__((import_module("env"), import_name("host_damage")))
extern void host_damage(int32_t handle, int32_t start_row, int32_t end_row, int32_t start_col, int32_t end_col);

__attribute__((import_module("env"), import_name("host_movecursor")))
extern void host_movecursor(int32_t handle, int32_t row, int32_t col, int32_t visible);

__attribute__((import_module("env"), import_name("host_settermprop")))
extern void host_settermprop(int32_t handle, int32_t prop, int32_t value, const char* str, int32_t str_len);

__attribute__((import_module("env"), import_name("host_bell")))
extern void host_bell(int32_t handle);

__attribute__((import_module("env"), import_name("host_pushline_cells")))
extern void host_pushline_cells(int32_t handle, const VTermScreenCell* cells, int32_t cols);

// Callback implementations
static int cb_damage(VTermRect rect, void* user) {
    int32_t handle = (int32_t)(uintptr_t)user;
    host_damage(handle, rect.start_row, rect.end_row, rect.start_col, rect.end_col);
    return 0;
}

static int cb_movecursor(VTermPos pos, VTermPos oldpos, int visible, void* user) {
    int32_t handle = (int32_t)(uintptr_t)user;
    host_movecursor(handle, pos.row, pos.col, visible);
    return 0;
}

static int cb_settermprop(VTermProp prop, VTermValue* val, void* user) {
    int32_t handle = (int32_t)(uintptr_t)user;
    
    // Determine value type based on property
    switch (prop) {
        case VTERM_PROP_TITLE:
        case VTERM_PROP_ICONNAME:
            // String property
            host_settermprop(handle, (int32_t)prop, 0, val->string.str, (int32_t)val->string.len);
            break;
        case VTERM_PROP_CURSORVISIBLE:
        case VTERM_PROP_CURSORBLINK:
        case VTERM_PROP_ALTSCREEN:
        case VTERM_PROP_REVERSE:
            // Boolean property
            host_settermprop(handle, (int32_t)prop, val->boolean ? 1 : 0, NULL, 0);
            break;
        case VTERM_PROP_CURSORSHAPE:
        case VTERM_PROP_MOUSE:
            // Number property
            host_settermprop(handle, (int32_t)prop, val->number, NULL, 0);
            break;
        default:
            break;
    }
    return 0;
}

static int cb_bell(void* user) {
    int32_t handle = (int32_t)(uintptr_t)user;
    host_bell(handle);
    return 0;
}

// Pass full cell array to Go for ANSI rendering
static int cb_pushline(int cols, const VTermScreenCell* cells, void* user) {
    int32_t handle = (int32_t)(uintptr_t)user;
    host_pushline_cells(handle, cells, cols);
    return 0;
}

// Static callbacks struct
static VTermScreenCallbacks wasm_screen_callbacks = {
    .damage = cb_damage,
    .moverect = NULL,
    .movecursor = cb_movecursor,
    .settermprop = cb_settermprop,
    .bell = cb_bell,
    .resize = NULL,
    .sb_pushline = cb_pushline,
    .sb_popline = NULL,
    .sb_clear = NULL,
};

// Export function to set callbacks with a handle
__attribute__((export_name("wasm_set_callbacks")))
void wasm_set_callbacks(VTermScreen* screen, int32_t handle) {
    vterm_screen_set_callbacks(screen, &wasm_screen_callbacks, (void*)(uintptr_t)handle);
}
