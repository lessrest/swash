/* SPDX-License-Identifier: LGPL-2.1-or-later */
/*
 * Minimal vendored sd-id128.h header for CGO compilation.
 * This provides only the type definitions needed by go-systemd.
 * The actual implementation comes from libsystemd at runtime.
 *
 * Original source: https://github.com/systemd/systemd
 * License: LGPL-2.1-or-later
 */

#ifndef _SD_ID128_H
#define _SD_ID128_H

#include <inttypes.h>

#define SD_ID128_STRING_MAX 33U

typedef union sd_id128 sd_id128_t;

union sd_id128 {
        uint8_t bytes[16];
        uint64_t qwords[2];
};

#endif /* _SD_ID128_H */
