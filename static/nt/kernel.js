import { call, createContext, once, resource, spawn, } from "effection";
import { tag } from "./tag.js";
export const Target = createContext("target", {
    node: document.body,
});
export const Widget = createContext("widget", {
    node: document.body,
});
export function* append(content) {
    const { node } = yield* Target;
    node.append(content);
}
export function* message(...content) {
    yield* append(tag("message", {}, ...content));
}
export function* setNode(element) {
    yield* Target.set({ node: element });
    return element;
}
export function* pushNode(node) {
    yield* append(node);
    yield* setNode(node);
    return node;
}
export function* waitForButton(label) {
    const button = tag("button", {}, label);
    yield* append(button);
    yield* once(button, "click");
    button.setAttribute("disabled", "");
}
export function* clear() {
    const { node } = yield* Target;
    node.replaceChildren();
}
export function* spawnWithElement(element, body) {
    return yield* spawn(function* () {
        return yield* body(yield* pushNode(element));
    });
}
export function* pushFramedWindow(title) {
    yield* pushNode(tag("div", { class: "window" }));
    yield* append(tag("header", { class: "title-bar" }, tag("span", { class: "title-bar-text" }, title)));
    return yield* pushNode(tag("div", { class: "window-body" }));
}
export function* spawnFramedWindow(title, body) {
    return yield* spawn(function* () {
        const window = yield* pushFramedWindow(title);
        try {
            return yield* body(window);
        }
        finally {
            window.setAttribute("failed", "");
        }
    });
}
export function* spawnFramedWindow2(title, body) {
    return yield* spawn(function* () {
        const window = yield* pushNode(tag("div", { class: "window2" }, title));
        try {
            return yield* body(window);
        }
        finally {
            window.setAttribute("failed", "");
        }
    });
}
export function useMediaStream(constraints) {
    return resource(function* (provide) {
        const stream = yield* call(navigator.mediaDevices.getUserMedia(constraints));
        try {
            yield* provide(stream);
        }
        finally {
            for (const track of stream.getTracks()) {
                yield* message(`stopping ${track.kind}`);
                track.stop();
            }
        }
    });
}
export function useMediaRecorder(stream, options) {
    return resource(function* (provide) {
        const recorder = new MediaRecorder(stream, options);
        try {
            yield* provide(recorder);
        }
        finally {
            if (recorder.state !== "inactive") {
                recorder.stop();
            }
        }
    });
}
