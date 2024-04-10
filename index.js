import { openDB } from "idb"
import { render } from "preact"
import { useState, useCallback, useRef, useEffect } from "preact/hooks"
import { html } from "htm/preact"
import { concatenateAudioBlobs } from "./audio.js"

const apiUrl = document.location.origin.replace(/^http/, "ws") + "/transcribe"

let state = {
  archive: [],
  transcript: [],
  current: { words: [], timestamp: null },
  interim: [],
  timeOfLastWord: null,
}

window.swash = {
  get state() {
    return state
  },
}

let recorderA = null // 100ms chunks
let recorderB = null // explicit stop

let audioStream = null
let transcriptionSocket = null

function Transcript({ transcript, current, interim }) {
  const currentWords = html`
    <${Interim} interim=${[...current.words, ...interim]} />
  `
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
      <${TranscriptItem}
        timestamp=${current.timestamp}
        words=${currentWords}
        button=${false}
        index=${transcript.length}
      />
      <button
        onClick=${startOrStop}
        className=${recorderA && recorderA.state === "recording"
          ? "stop"
          : "record"}
      ></button>
    </article>
  `
}

function Interim({ interim }) {
  const totalLength = interim
    .map(({ punctuated_word }) => punctuated_word)
    .join("").length

  // To show interim changes as a monotonically lengthening text,
  // we maintain a "rate of change" in characters/second and
  // when the buffer length exceeds the last buffer length
  // we set the rate of change, higher for larger additions
  // and lower for smaller additions.
  //
  // When the buffer length decreases, we immediately reset
  // the length limit to the shorter buffer length.
  //
  // The displayed text is the prefix of the buffer with length
  // equal to the length limit.

  const [lengthLimit, setLengthLimit] = useState(0)
  const [rateOfChange, setRateOfChange] = useState(0)

  const delta = totalLength - lengthLimit

  useEffect(() => {
    if (delta === 0) return

    const updateLengthLimit = () => {
      setLengthLimit((lengthLimit) => lengthLimit + 1)
      console.log("limit", lengthLimit)
    }

    const intervalId = setInterval(updateLengthLimit, 1000 / rateOfChange)

    return () => clearInterval(intervalId)
  }, [rateOfChange])

  useEffect(() => {
    const minRate = 10 // Minimum rate of change
    if (delta < 0) {
      setLengthLimit(totalLength)
      setRateOfChange(0)
    } else if (delta > 0) {
      const maxRate = 100
      // Rate of change is proportional to the size of the addition
      // let's say 50 is the expected max delta
      const newRate = Math.min(maxRate, minRate + delta)
      setRateOfChange(newRate)
    } else {
      setRateOfChange(0)
    }
    console.log("delta", delta, "rate", rateOfChange)
  }, [delta, totalLength])

  let choppedWords = []
  let budget = lengthLimit
  for (const word of interim) {
    if (word.punctuated_word.length > budget) {
      // chop the word
      const chopped = word.punctuated_word.slice(0, budget)
      choppedWords.push({ ...word, punctuated_word: chopped })
      break
    } else {
      choppedWords.push(word)
      budget -= word.punctuated_word.length
    }
  }

  console.log(choppedWords)

  const pendingIndicator = delta > 0 ? " 💬" : ""

  return html`<span class="interim"
    ><${Words} words=${choppedWords} />${pendingIndicator}</span
  >`
}

// function Words({ words }) {
//   return words.map(({ punctuated_word, confidence, speaker }) => {
//     return html`<span
//       style="opacity: ${confidence}"
//       class="speaker-${speaker}"
//       >${punctuated_word + " "}</span
//     >`
//   })
// }

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

function Words({ words }) {
  const getPauseForIndex = (i) => {
    if (i === 0) return 0
    const prev = words[i - 1]
    const current = words[i]
    return current.start - prev.end
  }

  const children = words.map(
    ({ punctuated_word, confidence, speaker }, i) => {
      const pauseSpan = html`<span
        class="pause"
        style="width: ${getPauseForIndex(i)}ch"
      ></span>`
      return html`<span
        style="filter: blur(${1 - confidence}px); opacity: ${confidence}"
        class="speaker-${speaker} word"
        key=${`word-${i}`}
        >${pauseSpan}${punctuated_word + " "}</span
      >`
    },
  )

  return html`<span class="words">${children}</span>`
}

function Whisper({ text, segments, prefix }) {
  if (segments) {
    const prefixTexts = prefix.map((segment) => segment.whisper.text)
    const prefixText = prefixTexts.slice(-3).join(" ")

    const parts = segments.map((segment, i) => {
      return html`<span class="whisper segment">${segment.text}</span>`
    })
    return html`<span class="whisper segments">${parts}</span>`
  } else {
    return html`<span class="whisper">${text}</span>`
  }
}

function WhisperButton({ audio, timestamp, prefix }) {
  const whisper = useCallback(
    () => doWhisper({ audio, timestamp, prefix }),
    [audio, timestamp, prefix],
  )

  // useEffect(() => {
  //   if (audio && mediaRecorder && mediaRecorder.state === "recording") {
  //     doWhisper({ audio, timestamp, prefix })
  //   }
  // }, [])

  return html` <button onClick=${whisper}>✨</button> `
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
      <nav>
        <span class="index">❡${index + 1}</span>
        <${Player} audio=${audio} />
        <button onClick=${() =>
          emit({ type: "SplitTranscript", timestamp })}>✂️</button>
        ${
          whisperButton
            ? html`<${WhisperButton}
                audio=${audio}
                timestamp=${timestamp}
                prefix=${prefix}
              />`
            : html`<span></span>`
        }
      </nav>
      <div style="display: flex; flex-direction: row; align-items: baseline; gap: 0.5rem">
        <span>${words}</span>
      </div>
      <time>${formatDateTimeHuman(new Date(timestamp))}</time>
    </p>
  `
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

function formatDateTimeHuman(date) {
  return date.toLocaleTimeString("en-US", {
    hour: "numeric",
    minute: "2-digit",
  })
}

//
// ╔═════════════════════════════════════════════════════╗
// ║ ✧┈╔═══╗┈✦┈╔═════╗┈✧┈╔═════════╗┈✦┈╔═════╗┈✧┈╔═══╗┈✦ ║
// ║ ┈┈║┈┈┈║┈┈┈║┈┈┈┈┈║┈┈┈║┈┈┈┈┈┈┈┈┈║┈┈┈║┈┈┈┈┈║┈┈┈║┈┈┈║┈┈ ║
// ║ ✦┈╚═══╝┈✧┈╚═════╝┈✦┈╚═════════╝┈✧┈╚═════╝┈✦┈╚═══╝┈✧ ║
// ╚═════════════════════════════════════════════════════╝
//
//        It's time for some... Event Sourcing™ ! 🎉
//
//      1. The world is the result of history.
//      2. History is a sequence of events.
//      3. Events are immutable facts.
//      4. The present is a function of past and event.
//

const handlers = {
  DeepgramMessage(state, { message }, timestamp) {
    if (message.type !== "Results") return state

    const { words, transcript } = message.channel.alternatives[0]

    if (transcript.trim() === "") return state

    const timeOfLastWord = Date.now()

    if (message.is_final) {
      // Move interim words to the current segment.
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
      // Update the interim words.
      return {
        ...state,
        interim: words,
        timeOfLastWord,
      }
    }
  },
  AudioBlob(state, { blob, data }, timestamp) {
    // Move the current segment to the transcript with audio attached.
    return {
      ...state,
      transcript: [
        ...state.transcript,
        {
          words: state.current.words,
          audio: URL.createObjectURL(blob || data),
          blob,
          t0: state.current.timestamp,
          timestamp,
        },
      ],
      current: { words: [], timestamp: null },
    }
  },
  StartedRecording(state) {
    return {
      ...state,
      timeOfLastWord: null,
    }
  },
  WhisperResult(state, { result, timestamp }) {
    // The timestamp here identifies the segment that was transcribed.
    // It's not the timestamp of the event itself.
    return {
      ...state,
      transcript: state.transcript.map((entry) =>
        entry.t0 === timestamp ? { ...entry, whisper: result } : entry,
      ),
    }
  },
  SplitTranscript(state, { timestamp }) {
    // Archive everything before the segment with the given timestamp.
    const archived = state.transcript.filter((entry) => entry.t0 < timestamp)
    const remaining = state.transcript.filter(
      (entry) => entry.t0 >= timestamp,
    )
    return {
      ...state,
      archive: [...state.archive, ...archived],
      transcript: remaining,
    }
  },
}

function reducer(state, { payload, timestamp }) {
  const handler = handlers[payload.type]
  return handler ? handler(state, payload, timestamp) : state
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

const allEvents = await getAllEvents()
console.log(allEvents.map((x) => x.payload.type))

for (const event of allEvents) {
  state = reducer(state, event)
}

show(state)

window.scrollTo(0, document.body.scrollHeight)

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

const audioBitsPerSecond = mimeType === "audio/mp4" ? 128000 : 32000

async function startRecording() {
  recorderA = new MediaRecorder(audioStream, {
    audioBitsPerSecond,
    mimeType,
  })

  recorderB = new MediaRecorder(audioStream, {
    audioBitsPerSecond,
    mimeType,
  })

  recorderA.ondataavailable = ({ data }) => {
    if (data.size > 0) {
      if (
        transcriptionSocket &&
        transcriptionSocket.readyState === WebSocket.OPEN
      ) {
        transcriptionSocket.send(data)
      }
    }
  }

  recorderA.onstop = () => {
    transcriptionSocket.send(JSON.stringify({ type: "CloseStream" }))
    recorderB.stop()
  }

  recorderB.onstart = () => emit({ type: "StartedRecording" })

  recorderB.ondataavailable = ({ data }) => {
    if (state.current.words.length > 0) {
      emit({ type: "AudioBlob", blob: data })
    }
  }

  recorderA.onerror = (error) => console.error("MediaRecorder", error)
  recorderB.onerror = (error) => console.error("MediaRecorder", error)

  await openTranscriptionSocket()

  recorderA.start(100)
  recorderB.start()
}

async function startOrStop() {
  if (recorderA && recorderA.state === "recording") {
    recorderA.stop()
  } else {
    audioStream = await navigator.mediaDevices.getUserMedia({ audio: true })
    await startRecording()
    setInterval(() => {
      if (recorderB.state === "recording") {
        if (
          state.timeOfLastWord &&
          Date.now() - state.timeOfLastWord > 10000
        ) {
          recorderB.stop()
          setTimeout(() => recorderB.start(), 0)
        }
      }
    }, 1000)
  }
}

window.addEventListener("beforeunload", () => {
  if (recorderA && recorderA.state === "recording") {
    recorderA.stop()
  }
})

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

  if (recorderA && recorderA.state === "recording") {
    window.scrollTo(0, document.body.scrollHeight)
  }
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

const openTranscriptionSocket = () =>
  new Promise((ok, no) => {
    transcriptionSocket = new WebSocket(apiUrl)

    transcriptionSocket.onopen = () => ok(transcriptionSocket)
    transcriptionSocket.onerror = (error) => no(error)
    transcriptionSocket.onmessage = (event) => {
      const message = JSON.parse(event.data)
      if (isBoringDeepgramMessage(message)) {
        return
      } else {
        emit({ type: "DeepgramMessage", message })
      }
    }
  })

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

async function deepgramWhisperRequest(audio) {
  const formData = new FormData()
  const blob = await fetch(audio).then((response) => response.blob())
  const extension = blob.type === "audio/mp4" ? "mp4" : "webm"

  formData.append("file", blob, `audio.${extension}`)

  const response = await fetch("/whisper-deepgram", {
    method: "POST",
    body: formData,
  })

  if (response.ok) {
    const result = await response.json()
    return result
  } else {
    throw new Error("Error calling Whisper API:", response.statusText)
  }
}
