// vterm-service: Terminal emulator over D-Bus
// Uses libvterm for screen state, exposes via sdbus-c++

#include <sdbus-c++/sdbus-c++.h>
#include <vterm.h>

#include "fcft_wrapper.h"
#include <pixman.h>
#include <png.h>

#include <pty.h>
#include <unistd.h>
#include <sys/wait.h>
#include <poll.h>
#include <signal.h>

#include <string>
#include <vector>
#include <thread>
#include <atomic>
#include <mutex>
#include <iostream>
#include <cstring>

class VTermService {
public:
    static constexpr const char* INTERFACE = "org.claude.VTerm";
    static constexpr const char* OBJECT_PATH = "/org/claude/VTerm";

    VTermService(sdbus::IConnection& conn, const std::string& busName,
                 int rows, int cols, const std::vector<std::string>& cmd)
        : conn_(conn), rows_(rows), cols_(cols), running_(false)
    {
        // Create libvterm instance
        vterm_ = vterm_new(rows, cols);
        vterm_set_utf8(vterm_, 1);

        screen_ = vterm_obtain_screen(vterm_);
        vterm_screen_reset(screen_, 1);

        // Register D-Bus object FIRST (before callbacks that emit signals)
        object_ = sdbus::createObject(conn, OBJECT_PATH);

        // Register signals
        object_->registerSignal(INTERFACE, "Damage", "ii");       // start_row, end_row
        object_->registerSignal(INTERFACE, "CursorMoved", "ii");  // row, col
        object_->registerSignal(INTERFACE, "Bell", "");
        object_->registerSignal(INTERFACE, "Exited", "i");        // exit_code
        object_->registerSignal(INTERFACE, "ScrollLine", "s");    // line text
        object_->registerSignal(INTERFACE, "TitleChanged", "s");  // title
        object_->registerSignal(INTERFACE, "ScreenMode", "b");    // is_alternate

        // Set up screen callbacks for signals AFTER object_ is ready
        static VTermScreenCallbacks callbacks = {
            .damage = [](VTermRect rect, void* user) -> int {
                auto* self = static_cast<VTermService*>(user);
                self->emitDamage(rect.start_row, rect.end_row);
                return 0;
            },
            .moverect = nullptr,
            .movecursor = [](VTermPos pos, VTermPos oldpos, int visible, void* user) -> int {
                auto* self = static_cast<VTermService*>(user);
                self->emitCursorMoved(pos.row, pos.col);
                return 0;
            },
            .settermprop = [](VTermProp prop, VTermValue* val, void* user) -> int {
                auto* self = static_cast<VTermService*>(user);
                if (prop == VTERM_PROP_TITLE) {
                    // Extract title string, handling length
                    if (val->string.str && val->string.len > 0) {
                        self->title_ = std::string(val->string.str, val->string.len);
                    } else {
                        self->title_.clear();
                    }
                    self->emitTitleChanged(self->title_);
                } else if (prop == VTERM_PROP_ALTSCREEN) {
                    self->alternateScreen_ = val->boolean;
                    self->emitScreenMode(self->alternateScreen_);
                }
                return 0;
            },
            .bell = [](void* user) -> int {
                auto* self = static_cast<VTermService*>(user);
                self->emitBell();
                return 0;
            },
            .resize = nullptr,
            .sb_pushline = [](int cols, const VTermScreenCell* cells, void* user) -> int {
                auto* self = static_cast<VTermService*>(user);
                std::string line = self->cellsToString(cols, cells);
                self->pushScrollback(line);
                self->emitScrollLine(line);
                return 0;
            },
            .sb_popline = nullptr,
        };
        vterm_screen_set_callbacks(screen_, &callbacks, this);
        vterm_screen_enable_altscreen(screen_, 1);

        // Methods
        object_->registerMethod(INTERFACE, "GetRowText", "i", "s",
            [this](sdbus::MethodCall call) {
                int row; call >> row;
                auto reply = call.createReply();
                reply << getRowText(row);
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetScreenText", "", "s",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << getScreenText();
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetScreenHtml", "", "s",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << getScreenHtml();
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetScreenData", "", "s",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << getScreenData();
                reply.send();
            });

        object_->registerMethod(INTERFACE, "SendInput", "s", "",
            [this](sdbus::MethodCall call) {
                std::string data; call >> data;
                sendInput(data);
                auto reply = call.createReply();
                reply.send();
            });

        object_->registerMethod(INTERFACE, "Kill", "i", "",
            [this](sdbus::MethodCall call) {
                int sig; call >> sig;
                sendSignal(sig);
                auto reply = call.createReply();
                reply.send();
            });

        object_->registerMethod(INTERFACE, "Resize", "ii", "",
            [this](sdbus::MethodCall call) {
                int r, c; call >> r >> c;
                resize(r, c);
                auto reply = call.createReply();
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetCursor", "", "ii",
            [this](sdbus::MethodCall call) {
                auto [row, col] = getCursor();
                auto reply = call.createReply();
                reply << row << col;
                reply.send();
            });

        object_->registerMethod(INTERFACE, "IsRunning", "", "b",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << running_.load();
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetExitCode", "", "i",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << exitCode_;
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetScrollback", "i", "as",
            [this](sdbus::MethodCall call) {
                int n; call >> n;
                auto reply = call.createReply();
                reply << getScrollback(n);
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetTitle", "", "s",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << title_;
                reply.send();
            });

        object_->registerMethod(INTERFACE, "GetMode", "", "b",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << alternateScreen_;
                reply.send();
            });

        object_->registerMethod(INTERFACE, "RenderPNG", "", "ay",
            [this](sdbus::MethodCall call) {
                auto reply = call.createReply();
                reply << renderPNG();
                reply.send();
            });

        object_->finishRegistration();

        // Enable signal emission now that D-Bus is ready
        signalsReady_.store(true);

        // Spawn the command
        if (!cmd.empty()) {
            spawnCommand(cmd);
        }
    }

    ~VTermService() {
        stop();
        if (font_) fcft_wrapper_destroy(font_);
        if (vterm_) vterm_free(vterm_);
        fcft_wrapper_fini();
    }

    void stop() {
        running_ = false;
        if (readerThread_.joinable()) {
            readerThread_.join();
        }
    }

private:
    // D-Bus connection (for emitting signals)
    sdbus::IConnection& conn_;

    // Terminal state
    VTerm* vterm_ = nullptr;
    VTermScreen* screen_ = nullptr;
    int rows_, cols_;
    std::mutex vtermMutex_;

    // Scrollback buffer
    std::vector<std::string> scrollback_;
    static constexpr size_t MAX_SCROLLBACK = 10000;

    // Terminal properties
    std::string title_;
    bool alternateScreen_ = false;

    // Font for rendering
    struct fcft_font* font_ = nullptr;
    int cellWidth_ = 0;
    int cellHeight_ = 0;

    // Process state
    int ptyMaster_ = -1;
    pid_t childPid_ = -1;
    std::atomic<bool> running_;
    int exitCode_ = -1;
    std::thread readerThread_;

    // D-Bus object
    std::unique_ptr<sdbus::IObject> object_;

    // Signal emission (called from vterm callbacks, potentially from reader thread)
    std::atomic<bool> signalsReady_{false};

    void emitDamage(int startRow, int endRow) {
        if (!signalsReady_.load()) return;
        auto signal = object_->createSignal(INTERFACE, "Damage");
        signal << startRow << endRow;
        object_->emitSignal(signal);
    }

    void emitCursorMoved(int row, int col) {
        if (!signalsReady_.load()) return;
        auto signal = object_->createSignal(INTERFACE, "CursorMoved");
        signal << row << col;
        object_->emitSignal(signal);
    }

    void emitBell() {
        if (!signalsReady_.load()) return;
        auto signal = object_->createSignal(INTERFACE, "Bell");
        object_->emitSignal(signal);
    }

    void emitExited(int code) {
        if (!signalsReady_.load()) return;
        auto signal = object_->createSignal(INTERFACE, "Exited");
        signal << code;
        object_->emitSignal(signal);
    }

    void emitScrollLine(const std::string& line) {
        if (!signalsReady_.load()) return;
        auto signal = object_->createSignal(INTERFACE, "ScrollLine");
        signal << line;
        object_->emitSignal(signal);
    }

    void emitTitleChanged(const std::string& title) {
        if (!signalsReady_.load()) return;
        auto signal = object_->createSignal(INTERFACE, "TitleChanged");
        signal << title;
        object_->emitSignal(signal);
    }

    void emitScreenMode(bool alternate) {
        if (!signalsReady_.load()) return;
        auto signal = object_->createSignal(INTERFACE, "ScreenMode");
        signal << alternate;
        object_->emitSignal(signal);
    }

    // Convert VTermScreenCell array to UTF-8 string
    std::string cellsToString(int cols, const VTermScreenCell* cells) {
        std::string result;
        for (int i = 0; i < cols; i++) {
            const auto& cell = cells[i];
            for (int j = 0; j < VTERM_MAX_CHARS_PER_CELL && cell.chars[j]; j++) {
                uint32_t c = cell.chars[j];
                if (c < 0x80) {
                    result += static_cast<char>(c);
                } else if (c < 0x800) {
                    result += static_cast<char>(0xC0 | (c >> 6));
                    result += static_cast<char>(0x80 | (c & 0x3F));
                } else if (c < 0x10000) {
                    result += static_cast<char>(0xE0 | (c >> 12));
                    result += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
                    result += static_cast<char>(0x80 | (c & 0x3F));
                } else {
                    result += static_cast<char>(0xF0 | (c >> 18));
                    result += static_cast<char>(0x80 | ((c >> 12) & 0x3F));
                    result += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
                    result += static_cast<char>(0x80 | (c & 0x3F));
                }
            }
            if (cell.chars[0] == 0) {
                result += ' ';
            }
        }
        // Trim trailing spaces
        while (!result.empty() && result.back() == ' ') {
            result.pop_back();
        }
        return result;
    }

    void pushScrollback(const std::string& line) {
        scrollback_.push_back(line);
        if (scrollback_.size() > MAX_SCROLLBACK) {
            scrollback_.erase(scrollback_.begin());
        }
    }

    std::vector<std::string> getScrollback(int n) {
        std::lock_guard<std::mutex> lock(vtermMutex_);
        if (n <= 0 || scrollback_.empty()) return {};
        size_t count = std::min(static_cast<size_t>(n), scrollback_.size());
        return std::vector<std::string>(
            scrollback_.end() - count,
            scrollback_.end()
        );
    }

    std::string getRowText(int row) {
        std::lock_guard<std::mutex> lock(vtermMutex_);

        if (row < 0 || row >= rows_) return "";

        std::string result;
        VTermPos pos = {row, 0};

        for (int col = 0; col < cols_; col++) {
            pos.col = col;
            VTermScreenCell cell;
            vterm_screen_get_cell(screen_, pos, &cell);

            // Get characters from cell
            for (int i = 0; i < VTERM_MAX_CHARS_PER_CELL && cell.chars[i]; i++) {
                // Convert UTF-32 to UTF-8
                uint32_t c = cell.chars[i];
                if (c < 0x80) {
                    result += static_cast<char>(c);
                } else if (c < 0x800) {
                    result += static_cast<char>(0xC0 | (c >> 6));
                    result += static_cast<char>(0x80 | (c & 0x3F));
                } else if (c < 0x10000) {
                    result += static_cast<char>(0xE0 | (c >> 12));
                    result += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
                    result += static_cast<char>(0x80 | (c & 0x3F));
                } else {
                    result += static_cast<char>(0xF0 | (c >> 18));
                    result += static_cast<char>(0x80 | ((c >> 12) & 0x3F));
                    result += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
                    result += static_cast<char>(0x80 | (c & 0x3F));
                }
            }

            // Handle empty cell
            if (cell.chars[0] == 0) {
                result += ' ';
            }
        }

        // Trim trailing spaces
        while (!result.empty() && result.back() == ' ') {
            result.pop_back();
        }

        return result;
    }

    std::string getScreenText() {
        std::string result;
        for (int row = 0; row < rows_; row++) {
            if (row > 0) result += '\n';
            result += getRowText(row);
        }
        return result;
    }

    // Helper to convert VTermColor to CSS rgb()
    std::string colorToCSS(VTermColor c) {
        if (VTERM_COLOR_IS_INDEXED(&c)) {
            vterm_screen_convert_color_to_rgb(screen_, &c);
        }
        char buf[32];
        snprintf(buf, sizeof(buf), "rgb(%d,%d,%d)", c.rgb.red, c.rgb.green, c.rgb.blue);
        return buf;
    }

    // HTML escape
    std::string htmlEscape(uint32_t ch) {
        if (ch == '<') return "&lt;";
        if (ch == '>') return "&gt;";
        if (ch == '&') return "&amp;";
        if (ch == '"') return "&quot;";
        if (ch < 32 || ch == 0) return " ";
        // UTF-8 encode
        std::string result;
        if (ch < 0x80) {
            result += static_cast<char>(ch);
        } else if (ch < 0x800) {
            result += static_cast<char>(0xC0 | (ch >> 6));
            result += static_cast<char>(0x80 | (ch & 0x3F));
        } else if (ch < 0x10000) {
            result += static_cast<char>(0xE0 | (ch >> 12));
            result += static_cast<char>(0x80 | ((ch >> 6) & 0x3F));
            result += static_cast<char>(0x80 | (ch & 0x3F));
        } else {
            result += static_cast<char>(0xF0 | (ch >> 18));
            result += static_cast<char>(0x80 | ((ch >> 12) & 0x3F));
            result += static_cast<char>(0x80 | ((ch >> 6) & 0x3F));
            result += static_cast<char>(0x80 | (ch & 0x3F));
        }
        return result;
    }

    std::string getScreenHtml() {
        std::lock_guard<std::mutex> lock(vtermMutex_);

        VTermColor defaultFg, defaultBg;
        vterm_state_get_default_colors(vterm_obtain_state(vterm_), &defaultFg, &defaultBg);

        std::string html = "<pre style=\"font-family:'JetBrains Mono',monospace;font-size:14px;line-height:1.2;background:";
        html += colorToCSS(defaultBg);
        html += ";margin:0;padding:8px;\">";

        for (int row = 0; row < rows_; row++) {
            if (row > 0) html += "\n";

            for (int col = 0; col < cols_; col++) {
                VTermPos pos = {row, col};
                VTermScreenCell cell;
                vterm_screen_get_cell(screen_, pos, &cell);

                VTermColor fg = cell.fg;
                VTermColor bg = cell.bg;
                if (VTERM_COLOR_IS_DEFAULT_FG(&fg)) fg = defaultFg;
                if (VTERM_COLOR_IS_DEFAULT_BG(&bg)) bg = defaultBg;

                if (cell.attrs.reverse) {
                    std::swap(fg, bg);
                }

                bool needSpan = !VTERM_COLOR_IS_DEFAULT_FG(&cell.fg) ||
                                !VTERM_COLOR_IS_DEFAULT_BG(&cell.bg) ||
                                cell.attrs.bold || cell.attrs.reverse;

                if (needSpan) {
                    html += "<span style=\"color:";
                    html += colorToCSS(fg);
                    if (!VTERM_COLOR_IS_DEFAULT_BG(&cell.bg) || cell.attrs.reverse) {
                        html += ";background:";
                        html += colorToCSS(bg);
                    }
                    if (cell.attrs.bold) {
                        html += ";font-weight:bold";
                    }
                    html += "\">";
                }

                html += htmlEscape(cell.chars[0] ? cell.chars[0] : ' ');

                if (needSpan) {
                    html += "</span>";
                }
            }
        }

        html += "</pre>";
        return html;
    }

    static std::string base64Encode(const std::vector<uint8_t>& data) {
        static const char* chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        std::string result;
        result.reserve((data.size() + 2) / 3 * 4);
        for (size_t i = 0; i < data.size(); i += 3) {
            uint32_t n = data[i] << 16;
            if (i + 1 < data.size()) n |= data[i + 1] << 8;
            if (i + 2 < data.size()) n |= data[i + 2];
            result += chars[(n >> 18) & 63];
            result += chars[(n >> 12) & 63];
            result += (i + 1 < data.size()) ? chars[(n >> 6) & 63] : '=';
            result += (i + 2 < data.size()) ? chars[n & 63] : '=';
        }
        return result;
    }

    std::string getScreenData() {
        std::lock_guard<std::mutex> lock(vtermMutex_);

        VTermColor defaultFg, defaultBg;
        vterm_state_get_default_colors(vterm_obtain_state(vterm_), &defaultFg, &defaultBg);

        std::string text;
        std::vector<uint8_t> fgData, bgData;
        fgData.reserve(rows_ * cols_ * 3);
        bgData.reserve(rows_ * cols_ * 3);

        for (int row = 0; row < rows_; row++) {
            if (row > 0) text += "\n";
            for (int col = 0; col < cols_; col++) {
                VTermPos pos = {row, col};
                VTermScreenCell cell;
                vterm_screen_get_cell(screen_, pos, &cell);

                VTermColor fg = cell.fg;
                VTermColor bg = cell.bg;
                if (VTERM_COLOR_IS_DEFAULT_FG(&fg)) fg = defaultFg;
                if (VTERM_COLOR_IS_DEFAULT_BG(&bg)) bg = defaultBg;
                if (VTERM_COLOR_IS_INDEXED(&fg)) vterm_screen_convert_color_to_rgb(screen_, &fg);
                if (VTERM_COLOR_IS_INDEXED(&bg)) vterm_screen_convert_color_to_rgb(screen_, &bg);

                if (cell.attrs.reverse) std::swap(fg, bg);

                // Encode bold in high bit of red
                uint8_t fgR = fg.rgb.red | (cell.attrs.bold ? 0x80 : 0);
                fgData.push_back(fgR);
                fgData.push_back(fg.rgb.green);
                fgData.push_back(fg.rgb.blue);
                bgData.push_back(bg.rgb.red);
                bgData.push_back(bg.rgb.green);
                bgData.push_back(bg.rgb.blue);

                // Plain text (escape < > &)
                uint32_t ch = cell.chars[0] ? cell.chars[0] : ' ';
                text += htmlEscape(ch);
            }
        }

        std::string html = "<pre data-rows=\"" + std::to_string(rows_) +
                           "\" data-cols=\"" + std::to_string(cols_) +
                           "\" data-fg=\"" + base64Encode(fgData) +
                           "\" data-bg=\"" + base64Encode(bgData) + "\">";
        html += text;
        html += "</pre>";
        return html;
    }

    // Initialize font for rendering (lazy init)
    bool initFont() {
        if (font_) return true;

        fcft_wrapper_init();

        // Try JetBrains Mono first, fall back to monospace
        font_ = fcft_wrapper_from_name("JetBrains Mono:size=14", NULL);
        if (!font_) {
            font_ = fcft_wrapper_from_name("monospace:size=14", NULL);
        }
        if (!font_) {
            std::cerr << "Failed to load font" << std::endl;
            return false;
        }

        cellWidth_ = fcft_wrapper_font_max_advance_x(font_);
        cellHeight_ = fcft_wrapper_font_height(font_);
        return true;
    }

    // Convert vterm color to pixman color
    pixman_color_t vtermToPixman(VTermColor c) {
        // Handle indexed colors
        if (VTERM_COLOR_IS_INDEXED(&c)) {
            vterm_screen_convert_color_to_rgb(screen_, &c);
        }
        return {
            static_cast<uint16_t>(c.rgb.red * 257),
            static_cast<uint16_t>(c.rgb.green * 257),
            static_cast<uint16_t>(c.rgb.blue * 257),
            0xFFFF
        };
    }

    // PNG write callback for in-memory output
    static void pngWriteCallback(png_structp png, png_bytep data, png_size_t len) {
        auto* output = static_cast<std::vector<uint8_t>*>(png_get_io_ptr(png));
        output->insert(output->end(), data, data + len);
    }

    // Render terminal to PNG using text runs (HarfBuzz shaping)
    std::vector<uint8_t> renderPNG() {
        std::lock_guard<std::mutex> lock(vtermMutex_);

        if (!initFont()) return {};

        int imgWidth = cols_ * cellWidth_;
        int imgHeight = rows_ * cellHeight_;

        // Create pixman image
        std::vector<uint32_t> pixels(imgWidth * imgHeight, 0xFF000000); // Black bg
        pixman_image_t* img = pixman_image_create_bits(
            PIXMAN_a8r8g8b8, imgWidth, imgHeight,
            pixels.data(), imgWidth * 4);

        // Default colors
        VTermColor defaultFg, defaultBg;
        vterm_state_get_default_colors(vterm_obtain_state(vterm_), &defaultFg, &defaultBg);

        // Render each row using text runs
        for (int row = 0; row < rows_; row++) {
            int y = row * cellHeight_;

            // First pass: fill backgrounds
            for (int col = 0; col < cols_; col++) {
                VTermPos pos = {row, col};
                VTermScreenCell cell;
                vterm_screen_get_cell(screen_, pos, &cell);

                int x = col * cellWidth_;

                VTermColor bg = cell.bg;
                if (VTERM_COLOR_IS_DEFAULT_BG(&bg)) bg = defaultBg;
                if (cell.attrs.reverse) {
                    VTermColor fg = cell.fg;
                    if (VTERM_COLOR_IS_DEFAULT_FG(&fg)) fg = defaultFg;
                    bg = fg;
                }

                pixman_color_t bgColor = vtermToPixman(bg);
                pixman_image_t* bgFill = pixman_image_create_solid_fill(&bgColor);
                pixman_image_composite32(PIXMAN_OP_SRC, bgFill, NULL, img,
                    0, 0, 0, 0, x, y, cellWidth_, cellHeight_);
                pixman_image_unref(bgFill);
            }

            // Build UTF-32 text for the row
            std::vector<uint32_t> rowText(cols_);
            for (int col = 0; col < cols_; col++) {
                VTermPos pos = {row, col};
                VTermScreenCell cell;
                vterm_screen_get_cell(screen_, pos, &cell);
                rowText[col] = cell.chars[0] ? cell.chars[0] : ' ';
            }

            // Rasterize as text run (enables ligatures)
            struct fcft_text_run* run = fcft_wrapper_rasterize_text_run(
                font_, cols_, rowText.data());

            if (run) {
                size_t count = fcft_wrapper_text_run_count(run);

                for (size_t i = 0; i < count; i++) {
                    const struct fcft_glyph* glyph = fcft_wrapper_text_run_glyph(run, i);
                    int cluster = fcft_wrapper_text_run_cluster(run, i);

                    // Get foreground color from the cell this glyph belongs to
                    VTermPos pos = {row, cluster};
                    VTermScreenCell cell;
                    vterm_screen_get_cell(screen_, pos, &cell);

                    VTermColor fg = cell.fg;
                    if (VTERM_COLOR_IS_DEFAULT_FG(&fg)) fg = defaultFg;
                    if (cell.attrs.reverse) {
                        VTermColor bg = cell.bg;
                        if (VTERM_COLOR_IS_DEFAULT_BG(&bg)) bg = defaultBg;
                        fg = bg;
                    }

                    if (glyph && fcft_wrapper_glyph_pix(glyph)) {
                        pixman_color_t fgColor = vtermToPixman(fg);
                        pixman_image_t* fgFill = pixman_image_create_solid_fill(&fgColor);
                        pixman_image_t* glyphPix = fcft_wrapper_glyph_pix(glyph);

                        // Position based on cluster (cell index) for grid alignment
                        int gx = cluster * cellWidth_ + fcft_wrapper_glyph_x(glyph);
                        int gy = y + fcft_wrapper_font_ascent(font_) - fcft_wrapper_glyph_y(glyph);

                        pixman_image_composite32(PIXMAN_OP_OVER,
                            fgFill, glyphPix, img,
                            0, 0, 0, 0, gx, gy,
                            fcft_wrapper_glyph_width(glyph), fcft_wrapper_glyph_height(glyph));

                        pixman_image_unref(fgFill);
                    }
                }
                fcft_wrapper_text_run_destroy(run);
            }
        }

        // Encode to PNG
        std::vector<uint8_t> output;
        png_structp png = png_create_write_struct(PNG_LIBPNG_VER_STRING, NULL, NULL, NULL);
        png_infop info = png_create_info_struct(png);

        png_set_write_fn(png, &output, pngWriteCallback, NULL);
        png_set_IHDR(png, info, imgWidth, imgHeight, 8,
            PNG_COLOR_TYPE_RGBA, PNG_INTERLACE_NONE,
            PNG_COMPRESSION_TYPE_DEFAULT, PNG_FILTER_TYPE_DEFAULT);
        png_write_info(png, info);

        // Convert ARGB to RGBA for PNG
        std::vector<uint8_t> rowBuf(imgWidth * 4);
        for (int y = 0; y < imgHeight; y++) {
            uint32_t* src = pixels.data() + y * imgWidth;
            for (int x = 0; x < imgWidth; x++) {
                uint32_t p = src[x];
                rowBuf[x*4+0] = (p >> 16) & 0xFF; // R
                rowBuf[x*4+1] = (p >> 8) & 0xFF;  // G
                rowBuf[x*4+2] = p & 0xFF;         // B
                rowBuf[x*4+3] = (p >> 24) & 0xFF; // A
            }
            png_write_row(png, rowBuf.data());
        }

        png_write_end(png, info);
        png_destroy_write_struct(&png, &info);
        pixman_image_unref(img);

        return output;
    }

    std::tuple<int, int> getCursor() {
        std::lock_guard<std::mutex> lock(vtermMutex_);
        VTermState* state = vterm_obtain_state(vterm_);
        VTermPos pos;
        vterm_state_get_cursorpos(state, &pos);
        return {pos.row, pos.col};
    }

    void sendInput(const std::string& data) {
        if (ptyMaster_ >= 0) {
            ::write(ptyMaster_, data.data(), data.size());
        }
    }

    void resize(int rows, int cols) {
        std::lock_guard<std::mutex> lock(vtermMutex_);
        rows_ = rows;
        cols_ = cols;
        vterm_set_size(vterm_, rows, cols);

        if (ptyMaster_ >= 0) {
            struct winsize ws = {
                .ws_row = static_cast<unsigned short>(rows),
                .ws_col = static_cast<unsigned short>(cols),
            };
            ioctl(ptyMaster_, TIOCSWINSZ, &ws);
        }
    }

    void spawnCommand(const std::vector<std::string>& cmd) {
        if (running_) return;
        if (cmd.empty()) return;

        // Create PTY
        struct winsize ws = {
            .ws_row = static_cast<unsigned short>(rows_),
            .ws_col = static_cast<unsigned short>(cols_),
        };

        childPid_ = forkpty(&ptyMaster_, nullptr, nullptr, &ws);

        if (childPid_ < 0) {
            return;
        }

        if (childPid_ == 0) {
            // Child process
            std::vector<char*> argv;
            for (const auto& arg : cmd) {
                argv.push_back(const_cast<char*>(arg.c_str()));
            }
            argv.push_back(nullptr);

            execvp(argv[0], argv.data());
            _exit(127);
        }

        // Parent - start reader thread
        running_ = true;
        readerThread_ = std::thread([this]() { readerLoop(); });
    }

    void sendSignal(int sig) {
        if (childPid_ > 0) {
            ::kill(childPid_, sig);
        }
    }

    void readerLoop() {
        char buf[4096];
        struct pollfd pfd = { ptyMaster_, POLLIN, 0 };

        while (running_) {
            int ret = poll(&pfd, 1, 100);  // 100ms timeout

            if (ret > 0 && (pfd.revents & POLLIN)) {
                ssize_t n = read(ptyMaster_, buf, sizeof(buf));
                if (n > 0) {
                    std::lock_guard<std::mutex> lock(vtermMutex_);
                    vterm_input_write(vterm_, buf, n);
                } else if (n <= 0) {
                    break;
                }
            }

            // Check if child exited
            int status;
            pid_t result = waitpid(childPid_, &status, WNOHANG);
            if (result == childPid_) {
                if (WIFEXITED(status)) {
                    exitCode_ = WEXITSTATUS(status);
                } else if (WIFSIGNALED(status)) {
                    exitCode_ = 128 + WTERMSIG(status);
                }
                emitExited(exitCode_);
                break;
            }
        }

        running_ = false;
        close(ptyMaster_);
        ptyMaster_ = -1;
    }
};

int main(int argc, char* argv[]) {
    std::string busName = "org.claude.VTerm";
    int rows = 24, cols = 80;
    std::vector<std::string> cmd;

    // Parse args
    for (int i = 1; i < argc; i++) {
        std::string arg = argv[i];
        if (arg == "--name" && i + 1 < argc) {
            busName = argv[++i];
        } else if (arg == "--rows" && i + 1 < argc) {
            rows = std::stoi(argv[++i]);
        } else if (arg == "--cols" && i + 1 < argc) {
            cols = std::stoi(argv[++i]);
        } else if (arg == "--") {
            // Everything after -- is the command
            for (int j = i + 1; j < argc; j++) {
                cmd.push_back(argv[j]);
            }
            break;
        }
    }

    auto conn = sdbus::createSessionBusConnection(busName);
    VTermService service(*conn, busName, rows, cols, cmd);

    std::cerr << "vterm-service started: " << busName
              << " (" << rows << "x" << cols << ")";
    if (!cmd.empty()) {
        std::cerr << " running: " << cmd[0];
    }
    std::cerr << std::endl;

    conn->enterEventLoop();

    return 0;
}
