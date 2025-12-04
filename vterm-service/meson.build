project('vterm-service', 'cpp', 'c',
  version : '0.1.0',
  default_options : ['cpp_std=c++20', 'c_std=c99'])

# Dependencies
sdbus_dep = dependency('sdbus-c++')

# libvterm (vendored)
libvterm_sources = files(
  'libvterm/src/encoding.c',
  'libvterm/src/keyboard.c',
  'libvterm/src/mouse.c',
  'libvterm/src/parser.c',
  'libvterm/src/pen.c',
  'libvterm/src/screen.c',
  'libvterm/src/state.c',
  'libvterm/src/unicode.c',
  'libvterm/src/vterm.c',
)

libvterm = static_library('vterm',
  libvterm_sources,
  include_directories : include_directories(
    'libvterm/include',
    'libvterm/src'
  ),
)

libvterm_dep = declare_dependency(
  link_with : libvterm,
  include_directories : include_directories('libvterm/include'),
)

# Main executable
executable('vterm-service',
  'vterm-service.cpp',
  dependencies : [sdbus_dep, libvterm_dep],
  install : true,
)
