// C wrapper for fcft to avoid C99/C++ incompatibilities
#include <fcft/fcft.h>

void fcft_wrapper_init(void) {
    fcft_init(FCFT_LOG_COLORIZE_AUTO, false, FCFT_LOG_CLASS_ERROR);
}

void fcft_wrapper_fini(void) {
    fcft_fini();
}

struct fcft_font* fcft_wrapper_from_name(const char* name, const char* attrs) {
    const char* names[] = { name };
    return fcft_from_name(1, names, attrs);
}

void fcft_wrapper_destroy(struct fcft_font* font) {
    fcft_destroy(font);
}

const struct fcft_glyph* fcft_wrapper_rasterize(struct fcft_font* font, uint32_t cp) {
    return fcft_rasterize_char_utf32(font, cp, FCFT_SUBPIXEL_NONE);
}

int fcft_wrapper_font_height(const struct fcft_font* font) {
    return font->height;
}

int fcft_wrapper_font_ascent(const struct fcft_font* font) {
    return font->ascent;
}

int fcft_wrapper_font_max_advance_x(const struct fcft_font* font) {
    return font->max_advance.x;
}

// Glyph accessors
pixman_image_t* fcft_wrapper_glyph_pix(const struct fcft_glyph* g) {
    return g->pix;
}

int fcft_wrapper_glyph_x(const struct fcft_glyph* g) { return g->x; }
int fcft_wrapper_glyph_y(const struct fcft_glyph* g) { return g->y; }
int fcft_wrapper_glyph_width(const struct fcft_glyph* g) { return g->width; }
int fcft_wrapper_glyph_height(const struct fcft_glyph* g) { return g->height; }
