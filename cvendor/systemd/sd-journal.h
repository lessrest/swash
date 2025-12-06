/* SPDX-License-Identifier: LGPL-2.1-or-later */
/*
 * Minimal vendored sd-journal.h header for CGO compilation.
 * This provides only the type definitions and constants needed by go-systemd.
 * The actual implementation comes from libsystemd at runtime.
 *
 * Original source: https://github.com/systemd/systemd
 * License: LGPL-2.1-or-later
 */

#ifndef _SD_JOURNAL_H
#define _SD_JOURNAL_H

#include <inttypes.h>
#include <stdarg.h>
#include <sys/types.h>

#include "sd-id128.h"

/* Journal open flags */
enum {
        SD_JOURNAL_LOCAL_ONLY                   = 1 << 0,
        SD_JOURNAL_RUNTIME_ONLY                 = 1 << 1,
        SD_JOURNAL_SYSTEM                       = 1 << 2,
        SD_JOURNAL_CURRENT_USER                 = 1 << 3,
        SD_JOURNAL_OS_ROOT                      = 1 << 4,
        SD_JOURNAL_ALL_NAMESPACES               = 1 << 5,
        SD_JOURNAL_INCLUDE_DEFAULT_NAMESPACE    = 1 << 6,
        SD_JOURNAL_TAKE_DIRECTORY_FD            = 1 << 7,
        SD_JOURNAL_ASSUME_IMMUTABLE             = 1 << 8
};

/* Journal wakeup event types */
enum {
        SD_JOURNAL_NOP        = 0,
        SD_JOURNAL_APPEND     = 1,
        SD_JOURNAL_INVALIDATE = 2
};

/* Opaque journal handle */
typedef struct sd_journal sd_journal;

#endif /* _SD_JOURNAL_H */
