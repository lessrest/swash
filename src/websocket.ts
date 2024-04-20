import {
  action,
  createChannel,
  createSignal,
  each,
  once,
  resource,
  spawn,
  type Operation,
  type Stream,
} from "effection"

export interface WebSocketHandle extends Stream<MessageEvent, CloseEvent> {
  send(value: string | ArrayBuffer | Blob | ArrayBufferView): Operation<void>
  close(code?: number, reason?: string): Operation<void>
}

export function useWebSocket(
  url: string | URL,
  protocols?: string | string[],
) {
  return resource<WebSocketHandle>(function* (provide) {
    let input = createChannel<string, { code?: number; reason?: string }>()
    let output = createSignal<MessageEvent, CloseEvent>()
    let socket = new WebSocket(url, protocols)

    yield* spawn(function* () {
      let cause = yield* once(socket, "error")
      throw new Error("WebSocket error", { cause })
    })

    yield* spawn(function* () {
      let inputs = yield* input
      let next = yield* inputs.next()

      while (!next.done) {
        socket.send(next.value)
        next = yield* inputs.next()
      }

      let { code, reason } = next.value
      socket.close(code, reason)
    })

    socket.onmessage = output.send
    socket.onclose = output.close

    yield* once(socket, "open")

    let handle: WebSocketHandle = {
      send: input.send,
      close: (code, reason) => input.close({ code, reason }),
      [Symbol.iterator]: output[Symbol.iterator],
    }

    //

    try {
      yield* action(function* (resolve) {
        yield* spawn(function* () {
          for (let _ of yield* each(output)) {
            yield* each.next()
          }
          resolve()
        })

        yield* spawn(function* () {
          for (let _ of yield* each(input)) {
            yield* each.next()
          }
          resolve()
        })

        yield* provide(handle)
      })
    } finally {
      socket.close(1000)
      if (socket.readyState !== socket.CLOSED) {
        yield* once(socket, "close")
      }
    }
  })
}

export interface ServerSentEventHandle extends Stream<MessageEvent, Event> {
  close(): Operation<void>
}

export function* useServerSentEvents(
  url: string,
): Operation<ServerSentEventHandle> {
  return yield* resource(function* (provide) {
    const eventSource = new EventSource(url)

    const output = createSignal<MessageEvent, Event>()
    const input = createChannel<void>()

    eventSource.onmessage = output.send
    eventSource.onerror = output.close

    yield* once(eventSource, "open")

    let handle: ServerSentEventHandle = {
      close: () => input.close(),
      [Symbol.iterator]: output[Symbol.iterator],
    }

    try {
      yield* action(function* (resolve) {
        yield* spawn(function* () {
          for (let _ of yield* each(output)) {
            yield* each.next()
          }
          resolve()
        })

        yield* spawn(function* () {
          for (let _ of yield* each(input)) {
            yield* each.next()
          }
          resolve()
        })

        yield* provide(handle)
      })
    } finally {
      eventSource.close()
    }
  })
}
