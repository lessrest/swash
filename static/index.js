import { createEventStore, saveEvent, getAllEvents } from "./eventStore.js"
import GraphemeSplitter from "grapheme-splitter"
import { render } from "preact"
import {
  useState,
  useCallback,
  useRef,
  useEffect,
  useMemo,
} from "preact/hooks"
import { signal, computed } from "@preact/signals"
import { html } from "htm/preact"
import { useSpeechAudio } from "./recording.js"
import { useLiveTranscription } from "./transcribing.js"
import { useChatCompletion } from "./chatCompletion.js"
import { useTypingEffect } from "./typing.js"

import { state, reducer, setState } from "./state.js"
import { prompts } from "./prompts.js"

function Session({ paragraphs, current, interim }) {
  const currentWords = html`
    <${Interim} interim=${[...current.words, ...interim]} />
  `
  const hasCurrentOrInterim = current.words.length > 0 || interim.length > 0

  const nonEmptySegments = paragraphs.filter(({ words }) => words.length > 0)

  return html`
    <article>
      ${nonEmptySegments.map(
        (segment, i) =>
          html`
            <${TranscriptSegment}
              segment=${segment}
              index=${i}
              segments=${nonEmptySegments}
              key=${`segment-${i}-${segment.timestamp}`} />
          `,
      )}
      ${hasCurrentOrInterim
        ? html`<${TranscriptItem}
            timestamp=${current.timestamp}
            words=${currentWords}
            button=${false}
            index=${nonEmptySegments.length} />`
        : ""}
      <br style="padding-top: 2rem" />
      <div class="recording-toolbar">
        <${RecordingWidget} />
      </div>
    </article>
  `
}

const splitter = new GraphemeSplitter()

function Interim({ interim }) {
  const text = interim.map(({ punctuated_word }) => punctuated_word).join(" ")
  const totalLength = splitter.splitGraphemes(text).length
  const displayedText = useTypingEffect(text)
  const displayedLength = splitter.splitGraphemes(displayedText).length
  const delta = totalLength - displayedLength

  let choppedWords = []
  let budget = displayedLength
  for (const word of interim) {
    const graphemes = splitter.splitGraphemes(word.punctuated_word)
    if (graphemes.length > budget) {
      // chop the word
      const chopped = graphemes.slice(0, budget).join("")
      choppedWords.push({ ...word, punctuated_word: chopped })
      break
    } else {
      choppedWords.push(word)
      budget -= graphemes.length
    }
  }

  const pendingIndicator = delta > 0 ? " 💬" : ""

  return html`<span class="interim"
    ><${Words} words=${choppedWords} />${pendingIndicator}</span
  >`
}

function TranscriptSegment({ segment, index, segments }) {
  const { words, audio, t0 } = segment

  const wordSpans = html`<${Words} words=${words} />`

  const text = words.map((word) => word.punctuated_word).join(" ")

  const messages = useMemo(() => {
    const previousMessages = segments
      .slice(0, index)
      .filter((entry) => entry.t0 < t0)
      .flatMap((entry) => [
        {
          role: "user",
          content: entry.words.map((word) => word.punctuated_word).join(" "),
        },
        { role: "assistant", content: entry.chatCompletion || "" },
      ])

    return [
      { role: "system", content: systemPrompt },
      ...previousMessages,
      { role: "user", content: text },
    ]
  }, [text, t0, JSON.stringify(segments)])

  const response = html`<${ChatCompletionSegment}
    t0=${t0}
    messages=${messages} />`

  return html`
    <${TranscriptItem}
      timestamp=${t0}
      audio=${audio}
      words=${wordSpans}
      index=${index}
      response=${response} />
  `
}

const promptSignal = signal("captainslog")
const systemPrompt = computed(() => prompts[promptSignal.value])

const models = {
  "gpt4-turbo": {
    name: "GPT IV Turbo",
    model: "gpt-4-turbo-preview",
    provider: "openai",
  },
  "claude3-opus": {
    name: "Claude III Opus",
    model: "claude-3-opus-20240229",
    provider: "anthropic",
  },
  "claude3-haiku": {
    name: "Claude III Haiku",
    model: "claude-3-haiku-20240307",
    provider: "anthropic",
  },
}

const modelSignal = signal("claude3-opus")

function ChatCompletionSegment({ messages, t0 }) {
  const onError = useCallback((error) => console.error(error), [])

  console.log(messages)

  const { isStreaming, isDone, message } = useChatCompletion({
    model: models[modelSignal.value],
    messages,
    temperature: 0,
    onError,
  })

  useEffect(() => {
    if (message && isDone) {
      emit({ type: "ChatCompletionResult", message: message.content, t0 })
    }
    // scroll to bottom
    window.scrollTo(0, document.body.scrollHeight)
  }, [message, isDone, t0])

  const displayedText = useTypingEffect(message ? message.content : "")
  const indicator = isStreaming ? " 💬" : isDone ? "" : " ⏳"

  console.log(message.content, "---", displayedText)

  return html`${displayedText}${indicator}`
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
  if (words.length === 0) return ""
  return html`
    <p>
      <nav>
        <span class="index">❡${index + 1}</span>
        <${Player} audio=${audio} />
        <button onClick=${() =>
          emit({ type: "SplitTranscript", timestamp })}>✂️</button>
      </nav>
      <div style="display: flex; flex-direction: column; gap: 0.5rem">
        <span>${words}</span>
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

const languageSignal = signal("en-US")

function RecordingWidget() {
  const [mediaStream, setMediaStream] = useState(null)

  return html`
    ${mediaStream === null
      ? html`<button
          class="record"
          onClick=${async () => {
            const stream = await getAudioStream()
            setMediaStream(stream)
          }}></button>`
      : ""}
    ${mediaStream
      ? html`<${RecordingInProgress} mediaStream=${mediaStream} />`
      : ""}
    <div
      style="display: flex; flex-direction: row; align-items: baseline; gap: 0.5rem">
      <select disabled=${!!mediaStream} value=${languageSignal.value}>
        <option value="en-US">English</option>
        <option value="sv-SE">Swedish</option>
      </select>
      <select disabled=${!!mediaStream} value=${modelSignal.value}>
        <option value="claude3-haiku">Claude III Haiku</option>
        <option value="claude3-opus">Claude III Opus</option>
        <option value="gpt4-turbo">GPT IV Turbo</option>
      </select>
      <select disabled=${!!mediaStream} value=${promptSignal.value}>
        ${Object.entries(prompts).map(([key, value]) => html`
          <option value=${key}>${key}</option>
        `)}
      </select>
    </div>
  `
}

async function getAudioStream() {
  return navigator.mediaDevices.getUserMedia({ audio: true })
}

function RecordingInProgress({ mediaStream }) {
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
    language: languageSignal.value,
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

  useEffect(() => {
    // start the recorder when the socket is open
    if (readyState === WebSocket.OPEN) {
      console.log("WebSocket open, starting recorder")
      start()
    }

    return () => {
      console.log("WebSocket closed, stopping recorder")
      stop()
    }
  }, [readyState, start, stop])

  switch (readyState) {
    case WebSocket.CONNECTING:
      console.log("WebSocket connecting, rendering connecting message")
      return html`<span>Connecting...</span>`
    case WebSocket.OPEN:
      console.log("WebSocket open, rendering RecordingInProgress")
      return html`<button class="stop" onClick=${stop}></button>`
    case WebSocket.CLOSING:
      console.log("WebSocket closing, rendering closing message")
      return html`<span>Closing connection...</span>`
    case WebSocket.CLOSED:
      console.log("WebSocket closed, rendering closed message")
      return html`<span>Connection closed.</span>`
    default:
      console.log("Unknown WebSocket state, rendering error message")
      return html`<span>Error: Unknown WebSocket state.</span>`
  }
}

const eventStore = await createEventStore()
const allEvents = [] // await getAllEvents(eventStore)
console.log(allEvents.map((x) => x.payload.type))

for (const event of allEvents) {
  setState(reducer(state, event))
}

show(state)

window.scrollTo(0, document.body.scrollHeight)

async function emit(payload) {
  const event = await saveEvent(eventStore, payload)
  update(event)
  console.info(payload.type, event, state)
}

function show(state) {
  render(
    html`
      <${Session}
        paragraphs=${state.paragraphs}
        current=${state.current}
        interim=${state.interim} />
    `,
    document.getElementById("app"),
  )

  // scroll to bottom
  window.scrollTo(0, document.body.scrollHeight)
}

function update(event) {
  setState(reducer(state, event))
  show(state)
}
