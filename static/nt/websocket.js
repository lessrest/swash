import { action, createChannel, createSignal, each, once, resource, spawn, } from "effection";
export function useWebSocket(url, protocols) {
    return resource(function* (provide) {
        let input = createChannel();
        let output = createSignal();
        let socket = new WebSocket(url, protocols);
        yield* spawn(function* () {
            let cause = yield* once(socket, "error");
            throw new Error("WebSocket error", { cause });
        });
        yield* spawn(function* () {
            let inputs = yield* input;
            let next = yield* inputs.next();
            while (!next.done) {
                socket.send(next.value);
                next = yield* inputs.next();
            }
            let { code, reason } = next.value;
            socket.close(code, reason);
        });
        socket.onmessage = output.send;
        socket.onclose = output.close;
        yield* once(socket, "open");
        let handle = {
            send: input.send,
            close: (code, reason) => input.close({ code, reason }),
            [Symbol.iterator]: output[Symbol.iterator],
        };
        //
        try {
            yield* action(function* (resolve) {
                yield* spawn(function* () {
                    for (let _ of yield* each(output)) {
                        yield* each.next();
                    }
                    resolve();
                });
                yield* spawn(function* () {
                    for (let _ of yield* each(input)) {
                        yield* each.next();
                    }
                    resolve();
                });
                yield* provide(handle);
            });
        }
        finally {
            socket.close(1000);
            if (socket.readyState !== socket.CLOSED) {
                yield* once(socket, "close");
            }
        }
    });
}
export function* useServerSentEvents(url) {
    return yield* resource(function* (provide) {
        const eventSource = new EventSource(url);
        const output = createSignal();
        const input = createChannel();
        eventSource.onmessage = output.send;
        eventSource.onerror = output.close;
        yield* once(eventSource, "open");
        let handle = {
            close: () => input.close(),
            [Symbol.iterator]: output[Symbol.iterator],
        };
        try {
            yield* action(function* (resolve) {
                yield* spawn(function* () {
                    for (let _ of yield* each(output)) {
                        yield* each.next();
                    }
                    resolve();
                });
                yield* spawn(function* () {
                    for (let _ of yield* each(input)) {
                        yield* each.next();
                    }
                    resolve();
                });
                yield* provide(handle);
            });
        }
        finally {
            eventSource.close();
        }
    });
}
