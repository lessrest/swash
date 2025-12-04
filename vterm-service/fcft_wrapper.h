// C wrapper for fcft
#pragma once

#include <pixman.h>
#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

struct fcft_font;
struct fcft_glyph;
struct fcft_text_run;

void fcft_wrapper_init(void);
void fcft_wrapper_fini(void);
struct fcft_font* fcft_wrapper_from_name(const char* name, const char* attrs);
void fcft_wrapper_destroy(struct fcft_font* font);
const struct fcft_glyph* fcft_wrapper_rasterize(struct fcft_font* font, uint32_t cp);

int fcft_wrapper_font_height(const struct fcft_font* font);
int fcft_wrapper_font_ascent(const struct fcft_font* font);
int fcft_wrapper_font_max_advance_x(const struct fcft_font* font);

pixman_image_t* fcft_wrapper_glyph_pix(const struct fcft_glyph* g);
int fcft_wrapper_glyph_x(const struct fcft_glyph* g);
int fcft_wrapper_glyph_y(const struct fcft_glyph* g);
int fcft_wrapper_glyph_width(const struct fcft_glyph* g);
int fcft_wrapper_glyph_height(const struct fcft_glyph* g);
int fcft_wrapper_glyph_advance_x(const struct fcft_glyph* g);

// Text run support (HarfBuzz shaping)
struct fcft_text_run* fcft_wrapper_rasterize_text_run(
    struct fcft_font* font, size_t len, const uint32_t* text);
void fcft_wrapper_text_run_destroy(struct fcft_text_run* run);
size_t fcft_wrapper_text_run_count(const struct fcft_text_run* run);
const struct fcft_glyph* fcft_wrapper_text_run_glyph(const struct fcft_text_run* run, size_t i);
int fcft_wrapper_text_run_cluster(const struct fcft_text_run* run, size_t i);

#ifdef __cplusplus
}
#endif
