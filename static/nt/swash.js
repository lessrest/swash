import { createChannel, each, main, on, sleep, spawn, } from "effection";
import { css } from "./css.js";
import { append, message, pushNode, setNode, spawnWithElement, useMediaRecorder, useMediaStream, } from "./kernel.js";
import { modelsByName, stream } from "./ntchat.js";
import { tag } from "./tag.js";
import { transcribe } from "./transcribe.js";
import { useWebSocket } from "./websocket.js";
function* typeMessage(text, confidence = 1) {
    yield* yield* spawnWithElement(tag("message", {
        style: `opacity: ${confidence}; text-decoration: ${confidence < 0.6 ? "line-through" : "none"}`,
    }), function* (self) {
        self.scrollIntoView({
            behavior: "smooth",
            block: "center",
            inline: "center",
        });
        requestAnimationFrame(() => {
            self.classList.add("started");
        });
        for (const char of text) {
            self.textContent += char;
            let delay = 20;
            if (char === "." || char === "!" || char === "?") {
                delay = 200 * Math.sin(Date.now() / 100) + 150;
            }
            else if (char === ",") {
                delay = 70 * Math.sin(Date.now() / 50) + 75;
            }
            yield* sleep(delay);
        }
        self.classList.add("finished");
    });
}
function* spawnVideoStream(stream, interimChannel, finalWordsChannel) {
    const article = tag("article", {}, tag("video", {
        srcObject: stream,
        autoplay: true,
        oncanplay: function () {
            this.muted = true;
        },
        onClick: function () {
            if (this.requestFullscreen) {
                this.requestFullscreen();
            }
        },
    }));
    yield* append(article);
    yield* spawnTextOverlay(article, interimChannel, finalWordsChannel);
}
function* spawnTextOverlay(article, interim, phrases) {
    yield* spawn(function* () {
        yield* setNode(article);
        yield* pushNode(tag("p"));
        let t = 0;
        let i = 1;
        let messages = [];
        for (let words of yield* each(phrases)) {
            const phrase = words.map(({ word }) => word).join(" ");
            if (phrase === "clear screen") {
                article.querySelectorAll("message").forEach((message) => {
                    message.remove();
                });
                yield* each.next();
                continue;
            }
            let t1 = Date.now();
            let dt = t ? t1 - t : 0;
            if (dt > 5000 || i === 1) {
                // if the last message didn't end with punctuation,
                // add an em dash
                const lastMessage = article.querySelector("message:last-child");
                if (lastMessage && lastMessage.textContent) {
                    const text = lastMessage.textContent.trim();
                    if (!text.match(/[.!?]/)) {
                        lastMessage.textContent = text + "—";
                    }
                }
                yield* append(tag("hr"));
                yield* append(tag("message", {
                    class: "started finished",
                    style: "font-size: 80%; padding: 0 1rem; opacity: 0.7; font-weight: bold;",
                }, `❡${i++} `));
            }
            messages.push({ role: "user", content: phrase });
            const subscription = yield* stream(modelsByName["Claude III Opus"], {
                temperature: 1,
                maxTokens: 512,
                systemMessage: `You are Claude III Opus, a noble and wise interlocutor, patient, concise, and calm. 
           Respond with a single sentence. Hesitate to offer help or advice.
           Primarily, you are here to acknowledge, mirror, and listen.`,
                messages,
            });
            for (const word of words) {
                yield* typeMessage(word.punctuated_word + " ", word.confidence);
            }
            yield* yield* spawn(function* () {
                yield* pushNode(tag("aside", {
                    style: "font-style: italic; white-space: pre-wrap;  padding: .25em .5em; font-size: 80%;",
                }));
                let next = yield* subscription.next();
                let response = "";
                while (!next.done && next.value) {
                    const { content } = next.value;
                    response += content;
                    console.info("Received response:", content);
                    yield* typeMessage(content, 1);
                    next = yield* subscription.next();
                }
                messages.push({ role: "assistant", content: response });
            });
            t = t1;
            yield* each.next();
        }
    });
}
function* recordingSession(stream) {
    const audioStream = new MediaStream(stream.getAudioTracks());
    let language = "en";
    const interimChannel = createChannel();
    const phraseChannel = createChannel();
    yield* spawnVideoStream(stream, interimChannel, phraseChannel);
    for (;;) {
        console.log("Starting session", language);
        const task = yield* spawn(function* () {
            const socket = yield* useWebSocket(`wss://swash2.less.rest/transcribe?language=${language}`);
            const recorder = yield* useMediaRecorder(audioStream, {
                mimeType: "audio/webm;codecs=opus",
                audioBitsPerSecond: 64000,
            });
            recorder.start(100);
            let blobs = [];
            yield* spawn(function* () {
                yield* foreach(on(recorder, "dataavailable"), function* ({ data }) {
                    blobs.push(data);
                    yield* socket.send(data);
                });
            });
            yield* spawnSocketMessageListener(socket, interimChannel, phraseChannel);
            yield* spawnVoiceCommandListener(phraseChannel, blobs);
            for (let words of yield* each(phraseChannel)) {
                const phrase = words.map(({ word }) => word).join(" ");
                if (phrase === "swedish please") {
                    language = "sv";
                    break;
                }
                else if (phrase === "engelska tack") {
                    language = "en";
                    break;
                }
                yield* each.next();
            }
        });
        yield* task;
    }
}
function* spawnVoiceCommandListener(finalWordsChannel, blobs) {
    yield* spawn(function* () {
        while (true) {
            yield* waitForSpecificPhrase(finalWordsChannel, "fix it up");
            const { words } = yield* transcribe(blobs);
            yield* message(words.map(({ punctuated_word }) => punctuated_word).join(" "));
        }
    });
    yield* spawn(function* () {
        yield* waitForSpecificPhrase(finalWordsChannel, "reload");
        document.location.reload();
    });
}
function* waitForSpecificPhrase(finalWordsChannel, phrase) {
    let subscription = yield* finalWordsChannel;
    while (true) {
        let { value } = yield* subscription.next();
        if (!value) {
            throw new Error("Channel closed");
        }
        else {
            const spokenPhrase = value.map(({ word }) => word).join(" ");
            if (spokenPhrase === phrase) {
                return;
            }
        }
    }
}
function* foreach(stream, callback) {
    for (let event of yield* each(stream)) {
        yield* callback(event);
        yield* each.next();
    }
}
function* spawnSocketMessageListener(socket, interimChannel, finalWordsChannel) {
    return yield* spawn(function* () {
        for (const event of yield* each(socket)) {
            const data = JSON.parse(event.data);
            if (data.type === "Results" && data.channel) {
                const { alternatives: [{ transcript, words }], } = data.channel;
                if (transcript) {
                    if (data.is_final) {
                        yield* finalWordsChannel.send(words);
                        yield* interimChannel.send([]);
                    }
                    else {
                        yield* interimChannel.send(words);
                    }
                }
            }
            yield* each.next();
        }
    });
}
function* app() {
    yield* pushNode(tag("app", {}, tag("style", {}, css)));
    const stream = yield* useMediaStream({ audio: true, video: true });
    for (;;) {
        yield* recordingSession(stream);
    }
}
document.addEventListener("DOMContentLoaded", async function () {
    await main(app);
});
