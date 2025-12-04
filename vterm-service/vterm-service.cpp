// vterm-service: Terminal emulator over D-Bus
// Uses libvterm for screen state, exposes via sdbus-c++

#include <sdbus-c++/sdbus-c++.h>
#include <vterm.h>

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
        if (vterm_) vterm_free(vterm_);
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
