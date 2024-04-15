import {
  Operation,
  action,
  call,
  createChannel,
  createContext,
  createSignal,
  each,
  ensure,
  main,
  on,
  once,
  resource,
  sleep,
  spawn,
  suspend,
} from "effection"

const bufferContext = createContext("buffer", document.body)

function* insert(content: string | Node) {
  const buffer = yield* bufferContext
  buffer.append(content)
}

function* message(...content: (string | Node)[]) {
  const element = document.createElement("message")
  element.append(...content)
  yield* insert(element)
}

function* enter(element: HTMLElement) {
  yield* bufferContext.set(element)
}

function useElement(tagName: string): Operation<HTMLElement> {
  return resource(function* (provide) {
    const element = document.createElement(tagName)
    try {
      yield* provide(element)
    } finally {
      element.remove()
    }
  })
}

function* style(css: string) {
  const element = yield* useElement("style")
  element.textContent = css
  yield* insert(element)
}

function* useResourceBuffer(title: string) {
  const element = yield* useElement("resource")
  yield* insert(element)
  yield* enter(element)
  const header = document.createElement("header")
  header.textContent = title
  yield* insert(header)
}

function* withResourceBuffer<T>(
  title: string,
  body: () => Operation<T>,
): Operation<T> {
  const task = yield* spawn(function* () {
    yield* useResourceBuffer(title)
    return yield* body()
  })
  return yield* task
}

function useWebSocket(url: string | URL): Operation<WebSocket> {
  return resource(function* (provide) {
    const socket = new WebSocket(url)

    try {
      yield* useResourceBuffer(`WebSocket ${socket.url}`)
      yield* spawn(function* () {
        yield* message("connecting")
        yield* once(socket, "open")
        yield* message("connected")
        yield* once(socket, "close")
        yield* message("closed")
      })
      yield* provide(socket)
    } finally {
      socket.close()
    }
  })
}

function useMediaStream(
  constraints: MediaStreamConstraints,
): Operation<MediaStream> {
  return resource(function* (provide) {
    const stream: MediaStream = yield* call(
      navigator.mediaDevices.getUserMedia(constraints),
    )
    yield* useResourceBuffer("MediaStream")
    try {
      yield* message(`stream ID: ${stream.id}`)
      for (const track of stream.getTracks()) {
        const { kind, id } = track
        yield* message(`track ${kind} ${id}`)
      }
      yield* spawn(function* () {
        for (const event of yield* each(on(stream, "addtrack"))) {
          yield* message(`track added: ${event.track.kind}`)
        }
      })
      yield* spawn(function* () {
        for (const event of yield* each(on(stream, "removetrack"))) {
          yield* message(`track removed: ${event.track.kind}`)
        }
      })
      yield* provide(stream)
    } finally {
      for (const track of stream.getTracks()) {
        yield* message(`stopping ${track.kind}`)
        track.stop()
      }
    }
  })
}

function useMediaRecorder(
  stream: MediaStream,
  options: MediaRecorderOptions,
): Operation<MediaRecorder> {
  return resource(function* (provide) {
    const recorder = new MediaRecorder(stream, options)
    yield* useResourceBuffer("MediaRecorder")
    try {
      yield* message(`recorder state: ${recorder.state}`)
      yield* spawn(function* () {
        for (const event of yield* each(on(recorder, "start"))) {
          yield* message(`started recording`)
          yield* each.next()
        }
      })
      yield* spawn(function* () {
        for (const event of yield* each(on(recorder, "stop"))) {
          yield* message(`stopped recording`)
          yield* each.next()
        }
      })
      yield* spawn(function* () {
        for (const event of yield* each(on(recorder, "dataavailable"))) {
          yield* each.next()
        }
      })
      yield* provide(recorder)
    } finally {
      if (recorder.state !== "inactive") {
        recorder.stop()
      }
    }
  })
}

function* actionButton(label: string): Operation<void> {
  const button = yield* useElement("button")
  button.textContent = label
  yield* message(button)
  yield* once(button, "click")
  button.setAttribute("disabled", "")
}

await main(function* () {
  yield* style(`
    message::before { content: "✱ "; }
    resource, message { display: flex; gap: 0.5rem; flex-wrap: wrap; }
    resource { flex-direction: column; }
    resource { border: 2px solid black; padding: 0.5rem; }
    resource > header { font-weight: bold; }
    resource > header::before { content: "🛜 "; }
  `)
  yield* useResourceBuffer("swa.sh")

  const stream = yield* useMediaStream({ audio: true, video: false })

  for (;;) {
    yield* actionButton("start")
    const blobs = yield* withResourceBuffer("session", function* () {
      const socket = yield* useWebSocket("wss://swash2.less.rest/transcribe")

      yield* once(socket, "open")
      yield* message("socket open")

      const recorder = yield* useMediaRecorder(stream, {
        mimeType: "audio/webm",
      })

      recorder.start(100)
      yield* message("recording")

      let blobs: Blob[] = []
      yield* spawn(function* () {
        for (const event of yield* each(on(recorder, "dataavailable"))) {
          blobs.push(event.data)
          socket.send(event.data)
          yield* each.next()
        }
      })

      // listen for socket messages
      yield* spawn(function* () {
        for (const event of yield* each(on(socket, "message"))) {
          const json = JSON.parse(event.data)
          console.log(json)
          if (json.type === "Results") {
            const { transcript } = json.channel.alternatives[0]
            if (transcript) {
              yield* message(transcript)
            }
          }
          yield* each.next()
        }
      })

      yield* actionButton("stop")
      recorder.stop()
      yield* message("stopped")
      return blobs
    })

    const audio = new Audio()
    audio.src = URL.createObjectURL(new Blob(blobs))
    audio.controls = true
    yield* message(audio)

    const formData = new FormData()
    formData.append("file", new Blob(blobs))

    const response = yield* call(
      fetch("/whisper-deepgram?language=en", {
        method: "POST",
        body: formData,
      }),
    )

    if (!response.ok) {
      throw new Error(`HTTP error! status: ${response.status}`)
    }

    const result = yield* call(response.json())
    console.log(result)
    const { words, transcript } = result.results.channels[0].alternatives[0]

    const wordSpans = document.createElement("span")
    for (const { punctuated_word, start, end, confidence } of words) {
      const innerSpan = document.createElement("span")
      innerSpan.textContent = punctuated_word + " "
      innerSpan.style.opacity = `${confidence}`
      wordSpans.appendChild(innerSpan)
    }
    yield* message(wordSpans)
  }
})
