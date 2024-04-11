import { createEventStore, saveEvent, getAllEvents } from "./eventStore.js"
import "grapheme-splitter"
import { render } from "preact"
import {
  useState,
  useCallback,
  useRef,
  useEffect,
  useMemo,
} from "preact/hooks"
import { html } from "htm/preact"
import { useSpeechAudio } from "./recording.js"
import { useLiveTranscription } from "./transcribing.js"
import { useChatCompletion } from "./chatCompletion.js"
import { useTypingEffect } from "./typing.js"

import { state, reducer } from "./state.js"

function Transcript({ transcript, current, interim }) {
  const currentWords = html`
    <${Interim} interim=${[...current.words, ...interim]} />
  `

  const nonEmptySegments = transcript.filter(({ words }) => words.length > 0)

  return html`
    <article>
      ${nonEmptySegments.map(
        (segment, i) =>
          html`
            <${TranscriptSegment}
              segment=${segment}
              index=${i}
              key=${`segment-${i}-${segment.timestamp}`} />
          `,
      )}
      <${TranscriptItem}
        timestamp=${current.timestamp}
        words=${currentWords}
        button=${false}
        index=${nonEmptySegments.length} />
      <${RecordingWidget} />
    </article>
  `
}

window.swash = {
  get state() {
    return state
  },
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

  const pendingIndicator = delta > 0 ? " 💬" : ""

  return html`<span class="interim"
    ><${Words} words=${choppedWords} />${pendingIndicator}</span
  >`
}

function TranscriptSegment({ segment, index }) {
  const { words, audio, t0 } = segment

  const wordSpans = html`<${Words} words=${words} />`

  const text = words.map((word) => word.punctuated_word).join(" ")
  // ask GPT
  const onError = useCallback((error) => console.error(error), [])
  const messages = useMemo(
    () => [
      { role: "system", content: "You are a helpful assistant." },
      {
        role: "user",
        content: `Write this text as a short sequence of terse bullet points: ${text}`,
      },
    ],
    [text],
  )

  const { isStreaming, isDone, message } = useChatCompletion({
    model: "gpt-4-turbo-preview",
    messages,
    temperature: 0,
    onError,
  })

  const displayedText = useTypingEffect(message ? message.content : "")

  return html`
    <${TranscriptItem}
      timestamp=${t0}
      audio=${audio}
      words=${wordSpans}
      index=${index}
      response=${displayedText} />
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
        style="width: ${getPauseForIndex(i)}ch"></span>`
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

function TranscriptItem({ timestamp, audio, words, index, response = "" }) {
  return html`
    <p>
      <nav>
        <span class="index">❡${index + 1}</span>
        <${Player} audio=${audio} />
        <button onClick=${() =>
          emit({ type: "SplitTranscript", timestamp })}>✂️</button>
      </nav>
      <div style="display: flex; flex-direction: row; align-items: baseline; gap: 0.5rem">
        <span>${words}</span>
      </div>
      <time>${formatDateTimeHuman(new Date(timestamp))}</time>
    </p>
    <p style="white-space: pre-wrap">
      <nav>
        <span class="index">✶${index + 1}</span>
      </nav>
      <div style="display: flex; flex-direction: row; align-items: baseline; gap: 0.5rem">
        <span class="response">${response}</span>
      </div>
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
      className=${playing ? "stop" : "play"}></button>
  `
}

function formatDateTimeHuman(date) {
  return date.toLocaleTimeString("en-US", {
    hour: "numeric",
    minute: "2-digit",
  })
}

function RecordingWidget() {
  const [mediaStream, setMediaStream] = useState(null)
  const [language, setLanguage] = useState("en-US")

  if (mediaStream) {
    return html`<${RecordingStarting}
      mediaStream=${mediaStream}
      language=${language} /> `
  } else {
    return html`<div
      style="display: flex; justify-content: space-between; align-items: center">
      <button
        class="record"
        onClick=${async () => {
          const stream = await getAudioStream()
          setMediaStream(stream)
        }}></button>
      <select
        value=${language}
        onChange=${(e) => setLanguage(e.target.value)}>
        <option value="en-US">English</option>
        <option value="sv-SE">Swedish</option>
      </select>
    </div>`
  }
}

async function getAudioStream() {
  return navigator.mediaDevices.getUserMedia({ audio: true })
}

function RecordingStarting({ mediaStream, language }) {
  const [deadline, setDeadline] = useState(null)

  const onUpdate = useCallback((message) => {
    console.log("Received Deepgram message:", message)
    emit({ type: "DeepgramMessage", message })
    setDeadline(Date.now() + 3000)
  }, [])

  const onError = useCallback((error) => {
    console.error("Deepgram error:", error)
    emit({ type: "DeepgramError", error })
  }, [])

  const { readyState, send } = useLiveTranscription({
    language,
    onUpdate,
    onError,
  })

  const onChunk = useCallback((blob) => {
    console.log("Received audio chunk:", blob)
    emit({ type: "AudioBlob", blob })
  }, [])

  const onFail = useCallback((error) => {
    console.error("Speech audio error:", error)
  }, [])

  const { start, stop, endCurrentChunk, isRecording } = useSpeechAudio({
    mediaStream,
    onFrame: send,
    onChunk,
    onFail,
  })

  useEffect(() => {
    if (deadline) {
      console.log("Setting deadline timeout")
      const id = setTimeout(endCurrentChunk, deadline - Date.now())
      return () => {
        console.log("Clearing deadline timeout")
        clearTimeout(id)
      }
    }
  }, [deadline, endCurrentChunk])

  useEffect(() => {
    if (isRecording) {
      console.log("Recording started, setting deadline")
      setDeadline(Date.now() + 3000)
    } else {
      console.log("Recording stopped, clearing deadline")
      setDeadline(null)
    }
  }, [isRecording])

  switch (readyState) {
    case WebSocket.CONNECTING:
      console.log("WebSocket connecting, rendering connecting message")
      return html`<p>Connecting...</p>`
    case WebSocket.OPEN:
      console.log("WebSocket open, rendering RecordingInProgress")
      return html`<${RecordingInProgress} start=${start} stop=${stop} />`
    case WebSocket.CLOSING:
      console.log("WebSocket closing, rendering closing message")
      return html`<p>Closing connection...</p>`
    case WebSocket.CLOSED:
      console.log("WebSocket closed, rendering closed message")
      return html`<p>Connection closed.</p>`
    default:
      console.log("Unknown WebSocket state, rendering error message")
      return html`<p>Error: Unknown WebSocket state.</p>`
  }
}

function RecordingInProgress({ start, stop }) {
  useEffect(() => {
    console.info("RecordingInProgress: Starting recorders")
    start()
    return () => {
      console.info("RecordingInProgress: Stopping recorders")
      stop()
    }
  }, [start, stop])

  return html`
    <div>
      <button class="stop" onClick=${stop}></button>
    </div>
  `
}

const handlers = {
  DeepgramMessage(state, { message }, timestamp) {
    if (message.type !== "Results") return state

    const { words, transcript } = message.channel.alternatives[0]

    if (transcript.trim() === "") return state

    if (message.is_final) {
      // Move interim words to the current segment.
      return {
        ...state,
        current: {
          words: [...state.current.words, ...words],
          timestamp,
        },
        interim: [],
      }
    } else {
      // Update the interim words.
      return {
        ...state,
        interim: words,
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

const eventStore = await createEventStore()
const allEvents = await getAllEvents(eventStore)
console.log(allEvents.map((x) => x.payload.type))

for (const event of allEvents) {
  state = reducer(state, event)
}

show(state)

window.scrollTo(0, document.body.scrollHeight)


async function emit(payload) {
  const event = await saveEvent(eventStore, payload)
  update(event)
  console.log(event, state)
}

function show(state) {
  render(
    html`
      <${Transcript}
        transcript=${state.transcript}
        current=${state.current}
        interim=${state.interim} />
    `,
    document.getElementById("app"),
  )
}

function update(event) {
  state = reducer(state, event)
  show(state)
}
