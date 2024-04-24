// deno-lint-ignore-file prefer-const
import { bundle } from "https://deno.land/x/emit@0.40.0/mod.ts"
import {
  Operation,
  Stream,
  action,
  call,
  createChannel,
  createSignal,
  each,
  main,
  resource,
  spawn,
  suspend,
  useAbortSignal,
  useScope,
} from "npm:effection@3.0.3"

import { Epoch, info, pushTaskName, task } from "./task.ts"
import { WebSocketHandle, useWebSocket } from "./websocket.ts"

import { DOMParser } from "https://deno.land/x/deno_dom@v0.1.45/deno-dom-wasm.ts"

globalThis.document = new DOMParser().parseFromString(
  ``,
  "text/html",
)! as unknown as Document

const deepgramApiKey = Deno.env.get("DEEPGRAM_API_KEY")!
const openaiApiKey = Deno.env.get("OPENAI_API_KEY")!
const anthropicApiKey = Deno.env.get("ANTHROPIC_API_KEY")!

function deepgramUrl(language: string): string {
  const queryParams = new URLSearchParams({
    model: "nova-2",
    interim_results: "true",
    smart_format: "true",
    vad_events: "false",
    diarize: "true",
    language,
  })
  return `wss://api.deepgram.com/v1/listen?${queryParams}`
}

function* handleTranscribe(
  req: Request,
  provide: (value: Response) => Operation<void>,
): Operation<void> {
  const url = new URL(req.url)
  const language = url.searchParams.get("language") || "en-US"

  yield* info("consumes stream of", "audio blobs")
  yield* info("produces stream of", "transcription events")

  const deepgramSocket = yield* useWebSocketClientStream(
    deepgramUrl(language),
    {
      headers: { Authorization: `Token ${deepgramApiKey}` },
    },
  )

  const { response, socket: browserSocket } = Deno.upgradeWebSocket(req)
  yield* info("became a web socket at", new Date())

  const browserHandle = yield* useWebSocket(browserSocket)

  const proxyProcessTask = yield* task(
    "a duplex proxy process",
    function* () {
      yield* spawn(function* () {
        yield* forwardMessages1(browserHandle, deepgramSocket)
      })

      yield* spawn(function* () {
        yield* forwardMessages2(deepgramSocket, browserHandle)
      })

      yield* suspend()
    },
  )

  yield* provide(response)

  yield* proxyProcessTask
}

function* forwardMessages1(
  from: WebSocketHandle,
  to: WebSocketStreamHandle,
): Operation<void> {
  let subscription = yield* from
  let next = yield* subscription.next()
  let i = 0
  let startTime = new Date().getTime()
  let endTime = 0
  let totalDataSize = 0

  while (!next.done) {
    if (i < 5) {
      if (typeof next.value.data === "string") {
        yield* info("ignored string message at", new Date())
        yield* subscription.next()
        continue
      } else {
        yield* info("received audio at", new Date())
        totalDataSize += next.value.data.byteLength
      }
    } else if (i == 5) {
      endTime = new Date().getTime()
      totalDataSize += next.value.data.byteLength
      const duration = (endTime - startTime) / 1000
      const averageWindowDuration = duration / 6
      const averageBitrate = totalDataSize / duration

      yield* info(
        "had average chunk duration",
        `${averageWindowDuration.toFixed(2)}s`,
      )
      yield* info(
        "had average audio bitrate",
        `${averageBitrate.toFixed(2)} bytes/s`,
      )
    }
    ++i

    if (typeof next.value.data === "string") {
      yield* to.send(next.value.data)
    } else {
      const uint8Array = new Uint8Array(next.value.data)
      yield* to.send(uint8Array)
    }
    next = yield* subscription.next()
  }
}

function* forwardMessages2(
  from: WebSocketStreamHandle,
  to: WebSocketHandle,
): Operation<void> {
  let subscription = yield* from
  let next = yield* subscription.next()
  let i = 0
  let startTime = new Date().getTime()
  let endTime = 0
  let totalDataSize = 0

  while (!next.done) {
    if (i < 5) {
      if (typeof next.value === "string") {
        yield* info("received JSON event at", new Date())
        totalDataSize += next.value.length
      } else {
        yield* info("ignored binary message at", new Date())
        yield* subscription.next()
        continue
      }
    } else if (i == 5) {
      endTime = new Date().getTime()
      totalDataSize += next.value.length

      const duration = (endTime - startTime) / 1000
      const averageEventDuration = duration / 6
      const averageBitrate = totalDataSize / duration

      yield* info(
        "had average event duration",
        `${averageEventDuration.toFixed(2)}s`,
      )
      yield* info(
        "had average event bitrate",
        `${averageBitrate.toFixed(2)} bytes/s`,
      )
    }
    ++i

    yield* to.send(next.value)
    next = yield* subscription.next()
  }
}

interface WebSocketStreamHandle
  extends Stream<string | Uint8Array, CloseEvent> {
  send: (msg: string | Uint8Array) => Operation<void>
  close: (code?: number, reason?: string) => Operation<void>
}

function* useWebSocketClientStream(
  url: string,
  options: { headers?: Record<string, string> },
): Operation<WebSocketStreamHandle> {
  return yield* resource(function* (provide) {
    yield* pushTaskName("an outgoing Deepgram WebSocket")

    const wss = new WebSocketStream(url, { headers: options.headers })

    const { readable, writable } = yield* call(wss.opened)
    yield* info("was opened at", new Date())

    const reader = readable.getReader()
    const writer = writable.getWriter()

    const input = createChannel<
      string | Uint8Array,
      { code?: number; reason?: string }
    >()
    const output = createSignal<string | Uint8Array, CloseEvent>()

    yield* spawn(function* () {
      // WebSocketStream.closed promise replaces WebSocket's onclose and onerror events.
      // On ungraceful severance, the promise rejects.
      // On graceful closure, it resolves with code and reason.
      const { code, reason } = yield* call(wss.closed)
      yield* info("was terminated by peer at", new Date())
      yield* info("had termination code", code)
      yield* info("had termination reason", reason)
    })

    yield* spawn(function* () {
      for (;;) {
        const { value, done } = yield* call(reader.read())
        if (done) break
        output.send(value)
      }
    })

    yield* spawn(function* () {
      let inputs = yield* input
      let next = yield* inputs.next()

      while (!next.done) {
        try {
          yield* call(writer.write(next.value))
          next = yield* inputs.next()
        } catch (error) {
          yield* info("error sending data", error)
          return
        }
      }

      yield* info("ran out of inputs at", new Date())
      wss.close(next.value)
    })

    try {
      let handle: WebSocketStreamHandle = {
        send: function* (msg: string | Uint8Array) {
          yield* input.send(msg)
        },
        close: function* (code?: number, reason?: string) {
          yield* input.close({ code, reason })
        },
        [Symbol.iterator]: output[Symbol.iterator],
      }

      yield* action(function* (resolve) {
        yield* spawn(function* () {
          for (let _ of yield* each(output)) {
            yield* each.next()
          }
          yield* info("output signal closed at", new Date())
          resolve()
        })

        yield* spawn(function* () {
          for (let _ of yield* each(input)) {
            yield* each.next()
          }
          yield* info("input signal closed at", new Date())
          resolve()
        })

        yield* provide(handle)
      })
    } finally {
      yield* info("requested socket closure at", new Date())
      wss.close()
      yield* call(wss.closed)
      yield* info("achieved socket closure at", new Date())
    }
  })
}

function* handleOpenAIProxy(
  req: Request,
  provide: (value: Response) => Operation<void>,
): Operation<void> {
  const path = new URL(req.url).pathname.replace("/openai", "")
  if (
    path !== "/v1/audio/transcriptions" &&
    path !== "/v1/chat/completions"
  ) {
    yield* provide(
      new Response("Unsupported OpenAI API endpoint", { status: 400 }),
    )
  }

  const res = yield* call(
    fetch(`https://api.openai.com${path}`, {
      method: req.method,
      headers: {
        "Authorization": `Bearer ${openaiApiKey}`,
        "Content-Type": req.headers.get("Content-Type") || "",
      },
      body: req.body,
      signal: yield* useAbortSignal(),
    }),
  )

  yield* provide(res)
}

function* handleAnthropicProxy(
  req: Request,
  provide: (value: Response) => Operation<void>,
): Operation<void> {
  const path = new URL(req.url).pathname.replace("/anthropic", "")
  if (path !== "/v1/messages") {
    yield* provide(
      new Response("Unsupported Anthropic API endpoint", { status: 400 }),
    )
  }

  const res = yield* call(
    fetch(`https://api.anthropic.com${path}`, {
      method: req.method,
      headers: {
        "X-API-Key": anthropicApiKey,
        "Content-Type": req.headers.get("Content-Type") || "",
        "anthropic-version": "2023-06-01",
      },
      body: req.body,
      signal: yield* useAbortSignal(),
    }),
  )

  yield* provide(res)
}

function* handleRequest(req: Request, seq: number): Operation<Response> {
  return yield* resource(function* (provide) {
    const url = new URL(req.url)
    yield* info("is an", "HTTP request handling process")
    const date = new Date()
    yield* info("began on", date)
    yield* Epoch.set(date)
    yield* info("has method", req.method)
    yield* info("has pathname", url.pathname)
    yield* info("has origin", "[redacted]")

    if (url.pathname === "/transcribe") {
      yield* handleTranscribe(req, provide)
    }

    if (url.pathname.startsWith("/openai/")) {
      yield* handleOpenAIProxy(req, provide)
    }

    if (url.pathname.startsWith("/anthropic/")) {
      yield* handleAnthropicProxy(req, provide)
    }

    if (url.pathname === "/") {
      const { code } = yield* call(
        bundle("./src/swash.ts", {
          importMap: {
            imports: {
              "effection": "https://esm.sh/effection@3.0.3?target=esnext",
              "grapheme-splitter":
                "https://esm.sh/grapheme-splitter@1.0.4?target=esnext",
            },
          },
          allowRemote: true,
          compilerOptions: {
            inlineSourceMap: true,
            inlineSources: true,
          },
          minify: true,
        }),
      )
      const css = yield* call(Deno.readTextFile("./static/swash.css"))
      yield* provide(
        new Response(
          `<!doctype html>
<meta charset=UTF-8 />
<meta name=viewport content="width=device-width" />
<title>swa.sh</title>
<style>
${css}
</style>
<script type="module">
${code}
</script>
`,
          { headers: { "content-type": "text/html" } },
        ),
      )
    }

    yield* provide(new Response("Not found", { status: 404 }))
  })
}

const port = parseInt(Deno.env.get("PORT") || "8080")
const hostname = Deno.env.get("HOST")

function* serve() {
  let requestId = 1
  const scope = yield* useScope()
  const server = Deno.serve(
    { port, hostname },
    (req) =>
      new Promise((resolve, reject) => {
        const myRequestId = requestId++
        scope.run(function* () {
          yield* pushTaskName(`request #${myRequestId}`)
          try {
            const response = yield* handleRequest(req, myRequestId)
            yield* info("sent HTTP", response.status, "at", new Date())
            resolve(response)
            yield* suspend()
            yield* info("succeeded at", new Date())
          } catch (err) {
            yield* info("failed at", new Date())
            reject(err)
          }
        })
      }),
  )

  try {
    yield* call(server.finished)
  } finally {
    yield* call(server.shutdown())
  }
}

await main(function* () {
  yield* yield* task("swa.sh server", serve)
})
