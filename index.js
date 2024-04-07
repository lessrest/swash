import { openDB } from "idb"
import { render } from "preact"
import { useState, useCallback, useRef, useEffect } from "preact/hooks"
import { html } from "htm/preact"

const apiUrl = document.location.origin.replace(/^http/, "ws") + "/transcribe"

window.state = {
  transcript: [],
  current: { words: [], timestamp: null },
  interim: [],
  timeOfLastWord: null,
}

const storeName = "events.2"

const eventStore = await openDB("swa.sh", 3, {
  upgrade: (db, oldVersion, newVersion, transaction) => {
    db.createObjectStore(storeName, {
      keyPath: "sequenceNumber",
      autoIncrement: true,
    })
  },
})

for (const event of await getAllEvents()) {
  //  console.log(event)
  state = reducer(state, event)
}

show(state)

async function saveEvent(payload) {
  const key = await eventStore.add(storeName, {
    timestamp: Date.now(),
    payload,
  })

  return await eventStore.get(storeName, key)
}

async function getAllEvents() {
  return await eventStore.getAll(storeName)
}

async function emit(payload) {
  const event = await saveEvent(payload)
  update(event)
  console.log(event, state)
}

const mimeType = MediaRecorder.isTypeSupported("audio/webm;codecs=opus")
  ? "audio/webm;codecs=opus"
  : "audio/mp4"

let mediaRecorder = null
let stream = null
let socket = null

async function startRecording() {
  await connectToTranscriptionAPI()

  mediaRecorder = new MediaRecorder(stream, {
    audioBitsPerSecond: 128000,
    mimeType,
  })

  let chunks = []

  mediaRecorder.ondataavailable = ({ data }) => {
    if (data.size > 0) {
      chunks.push(data)
      if (socket && socket.readyState === WebSocket.OPEN) {
        socket.send(data)
      }
    }
  }

  mediaRecorder.onstop = () => {
    if (state.current.words.length > 0) {
      const blob = new Blob(chunks, { type: mimeType })
      emit({ type: "AudioBlob", blob })
    }

    socket.close()
    socket = null
    mediaRecorder = null
    chunks = []
  }

  mediaRecorder.onerror = (error) =>
    console.error("MediaRecorder error:", error)

  mediaRecorder.start(100)

  await emit({ type: "StartedRecording" })
}

async function start() {
  if (mediaRecorder && mediaRecorder.state === "recording") {
    mediaRecorder.stop()
  } else {
    stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    await startRecording()
    setInterval(() => {
      if (mediaRecorder.state === "recording") {
        if (
          state.timeOfLastWord &&
          Date.now() - state.timeOfLastWord > 3000
        ) {
          console.log("stop")
          mediaRecorder.stop()
          setTimeout(startRecording, 0)
        }
      }
    }, 1000)
  }
}

function Interim({ interim }) {
  return html`<p>
    <button onClick=${start} className="record"></button>
    <kbd>${interim.map(({ word }) => word + " ")}</kbd>
  </p>`
}

function show(state) {
  render(
    html`
      <${Transcript}
        transcript=${state.transcript}
        current=${state.current}
        interim=${state.interim}
      />
    `,
    document.getElementById("app"),
  )

  window.scrollTo(0, document.body.scrollHeight)
}

function update(event) {
  state = reducer(state, event)
  show(state)
}

function isBoringDeepgramMessage(message) {
  if (message.channel && message.channel.alternatives) {
    return (
      message.channel.alternatives[0].confidence === 0 &&
      message.channel.alternatives[0].transcript === ""
    )
  }
  return false
}

const connectToTranscriptionAPI = () =>
  new Promise((ok, no) => {
    socket = new WebSocket(apiUrl)

    socket.onopen = () => ok(socket)
    socket.onerror = (error) => no(error)
    socket.onmessage = (event) => {
      const message = JSON.parse(event.data)
      if (isBoringDeepgramMessage(message)) {
        return
      } else {
        emit({ type: "DeepgramMessage", message })
      }
    }
  })

function reducer(state, { payload, timestamp }) {
  if (payload.type === "DeepgramMessage") {
    if (payload.message.type !== "Results") return state
    const { words, transcript } = payload.message.channel.alternatives[0]
    const isFinal = payload.message.is_final

    if (transcript.trim() === "") return state

    const timeOfLastWord = Date.now()

    if (isFinal) {
      return {
        ...state,
        current: {
          words: [...state.current.words, ...words],
          timestamp,
        },
        interim: [],
        timeOfLastWord,
      }
    } else {
      return {
        ...state,
        interim: words,
        timeOfLastWord,
      }
    }
  } else if (payload.type === "AudioBlob") {
    return {
      ...state,
      transcript: [
        ...state.transcript,
        {
          words: state.current.words,
          audio: URL.createObjectURL(payload.blob),
          t0: state.current.timestamp,
          timestamp,
        },
      ],
      current: { words: [], timestamp: null },
    }
  } else if (payload.type === "StartedRecording") {
    return {
      ...state,
      timeOfLastWord: null,
    }
  } else if (payload.type === "WhisperResult") {
    return {
      ...state,
      transcript: state.transcript.map((entry) =>
        entry.t0 === payload.timestamp
          ? { ...entry, whisper: payload.result }
          : entry,
      ),
    }
  } else {
    return state
  }
}

function Player({ audio }) {
  const [playing, setPlaying] = useState(false)
  const audioRef = useRef(null)

  useEffect(() => {
    audioRef.current = new Audio(audio)
    audioRef.current.onended = () => setPlaying(false)
    return () => audioRef.current.pause()
  }, [audio])

  const play = useCallback(() => {
    audioRef.current.play()
    setPlaying(true)
  }, [])

  const stop = useCallback(() => {
    audioRef.current.pause()
    audioRef.current.currentTime = 0
    setPlaying(false)
  }, [])

  return html`
    <button
      onClick=${playing ? stop : play}
      className=${playing ? "stop" : "play"}
    ></button>
  `
}

function Words({ words }) {
  return words.map(({ word, confidence, speaker }) => {
    return html`<span
      style="opacity: ${confidence}"
      class="speaker-${speaker}"
      >${word + " "}</span
    >`
  })
}

function Whisper({ text, segments, prefix }) {
  if (segments) {
    const prefixTexts = prefix.map((segment) => segment.whisper.text)
    const prefixText = prefixTexts.slice(-3).join(" ")
    console.log(prefixText)
    const parts = segments.map((segment, i) => {
      return html`<span class="whisper segment">${segment.text}</span>`
    })
    return html`<span class="whisper segments">${parts}</span>`
  } else {
    return html`<span class="whisper">${text}</span>`
  }
}

function formatDateTimeHuman(date) {
  return date.toLocaleTimeString("en-US", {
    hour: "numeric",
    minute: "2-digit",
  })
}

function Transcript({ transcript, current }) {
  const currentWords = html`<${Words} words=${current.words} />`
  let prefix = []
  return html`
    <article>
      ${transcript.map((segment, i) => {
        const x = html`
          <${TranscriptSegment}
            prefix=${prefix}
            segment=${segment}
            index=${i}
            key=${`segment-${i}-${segment.timestamp}`}
          />
        `
        prefix = segment.whisper ? [...prefix, segment] : [...prefix]
        return x
      })}
      ${current.words.length > 0 &&
      html`<${TranscriptItem}
        timestamp=${current.timestamp}
        words=${currentWords}
        button=${false}
        index=${transcript.length}
      />`}
      <${Interim} interim=${state.interim} />
    </article>
  `
}

function TranscriptSegment({ segment, index, prefix }) {
  const { words, audio, timestamp, whisper, t0 } = segment

  const wordSpans = whisper
    ? html`<${Whisper}
        text=${whisper.text}
        segments=${whisper.segments}
        prefix=${prefix}
      />`
    : html`<${Words} words=${words} />`

  const hasWhisper = whisper && whisper.text

  return html`
    <${TranscriptItem}
      timestamp=${t0}
      audio=${audio}
      whisperButton=${true}
      words=${wordSpans}
      prefix=${prefix}
      index=${index}
    />
  `
}

function TranscriptItem({
  timestamp,
  audio,
  whisperButton,
  words,
  index,
  prefix,
}) {
  return html`
    <p>
      <${Player} audio=${audio} />
      <span class="index">❡${index + 1}</span>
      ${
        whisperButton
          ? html`<${WhisperButton}
              audio=${audio}
              timestamp=${timestamp}
              prefix=${prefix}
            />`
          : html`<span></span>`
      }
      <div style="display: flex; flex-direction: row; align-items: baseline; gap: 0.5rem">
        <span>${words}</span>
      </div>
      <time>${formatDateTimeHuman(new Date(timestamp))}</time>
    </p>
  `
}

async function doWhisper({ audio, timestamp, prefix }) {
  const formData = new FormData()
  const blob = await fetch(audio).then((response) => response.blob())
  const extension = blob.type === "audio/mp4" ? "mp4" : "webm"

  const prefixTexts = prefix.map((segment) => segment.whisper.text)
  const prefixText = prefixTexts.slice(-3).join(" ")

  formData.append("file", blob, `audio.${extension}`)
  formData.append("prefix", prefixText)

  const response = await fetch("/whisper", {
    method: "POST",
    body: formData,
  })

  if (response.ok) {
    const result = await response.json()
    emit({ type: "WhisperResult", result, timestamp, verbose: true })
  } else {
    console.error("Error calling Whisper API:", response.statusText)
  }
}

function WhisperButton({ audio, timestamp, prefix }) {
  const whisper = useCallback(
    () => doWhisper({ audio, timestamp, prefix }),
    [audio, timestamp, prefix],
  )

  useEffect(() => {
    if (audio && mediaRecorder && mediaRecorder.state === "recording") {
      whisper()
    }
  }, [])

  return html` <button onClick=${whisper}>✨</button> `
}
