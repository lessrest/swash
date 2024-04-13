import { createEventStore, saveEvent, getAllEvents } from "./eventStore.js"
import GraphemeSplitter from "grapheme-splitter"
import { render } from "preact"
import { useState, useCallback, useEffect, useMemo } from "preact/hooks"
import { computed, signal } from "@preact/signals"
import { html } from "htm/preact"
import { useSpeechAudio } from "./recording.js"
import { useLiveTranscription } from "./transcribing.js"
import { useChatCompletion } from "./chatCompletion.js"
import { useTypingEffect } from "./typing.js"

import { reducer } from "./state.js"
import { prompts as initialPrompts } from "./prompts.js"

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

const firstAppState = () => ({
  promptName: "captainslog",
  model: "claude3-opus",
  language: "en-US",
  paragraphs: [],
  current: { words: [], timestamp: null },
  interim: [],
  prompts: initialPrompts,
})

const appState = signal(firstAppState())

const currentWords = computed(() => [
  ...appState.value.current.words,
  ...appState.value.interim,
])

const paragraphs = computed(() => appState.value.paragraphs)
const nonEmptyParagraphs = computed(() =>
  paragraphs.value.filter(({ words }) => words.length > 0),
)
const promptName = computed(() => appState.value.promptName)
const model = computed(() => appState.value.model)
const language = computed(() => appState.value.language)
const prompts = computed(() => appState.value.prompts)
const currentTimestamp = computed(() => appState.value.current.timestamp)

function Session() {
  const textAnimation = html`
    <${AnimatedWords} interim=${currentWords.value} />
  `

  return html`
    <article>
      ${nonEmptyParagraphs.value.map(
        (segment, i) =>
          html`
            <${Paragraph}
              segment=${segment}
              index=${i}
              key=${`segment-${i}-${segment.timestamp}`} />
          `,
      )}
      ${currentWords.value.length > 0
        ? html`<${TranscriptItem}
            timestamp=${currentTimestamp.value}
            words=${textAnimation}
            button=${false}
            index=${nonEmptyParagraphs.value.length} />`
        : ""}
      <br style="padding-top: 2rem" />
      <div class="recording-toolbar">
        <${Toolbar} />
      </div>
    </article>
  `
}

function Toolbar() {
  const [mediaStream, setMediaStream] = useState(null)

  const changeLanguage = useCallback((e) => {
    appState.value = {
      ...appState.value,
      language: e.target.value,
    }
  }, [])

  const changeModel = useCallback((e) => {
    appState.value = {
      ...appState.value,
      model: e.target.value,
    }
  }, [])

  const changePrompt = useCallback((e) => {
    appState.value = {
      ...appState.value,
      promptName: e.target.value,
    }
  }, [])

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
      <select
        disabled=${!!mediaStream}
        value=${language.value}
        onChange=${changeLanguage}>
        <option value="en-US">English</option>
        <option value="sv-SE">Swedish</option>
      </select>
      <select
        disabled=${!!mediaStream}
        value=${model.value}
        onChange=${changeModel}>
        <option value="claude3-haiku">Claude III Haiku</option>
        <option value="claude3-opus">Claude III Opus</option>
        <option value="gpt4-turbo">GPT IV Turbo</option>
      </select>
      <select
        disabled=${!!mediaStream}
        value=${promptName.value}
        onChange=${changePrompt}>
        ${Object.entries(prompts.value).map(
          ([key, value]) => html` <option value=${key}>${key}</option> `,
        )}
      </select>
    </div>
  `
}

const splitter = new GraphemeSplitter()

function AnimatedWords({ interim }) {
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

function Paragraph({ segment, index }) {
  const { words, t0 } = segment

  const wordSpans = html`<${Words} words=${words} />`
  const text = words.map((word) => word.punctuated_word).join(" ")
  const systemPrompt = prompts.value[promptName.value]

  const previousMessages = useMemo(
    () =>
      nonEmptyParagraphs.value
        .slice(0, index)
        .filter((entry) => entry.t0 < t0)
        .flatMap((entry) => [
          {
            role: "user",
            content: entry.words
              .map((word) => word.punctuated_word)
              .join(" "),
          },
          ...(entry.chatCompletion
            ? [{ role: "assistant", content: entry.chatCompletion }]
            : []),
        ]),
    [nonEmptyParagraphs.value, t0],
  )

  const messages = useMemo(
    () => [
      { role: "system", content: systemPrompt },
      ...previousMessages,
      { role: "user", content: text },
    ],
    [text, systemPrompt, previousMessages],
  )

  const response = html`<${ChatCompletionSegment}
    t0=${t0}
    messages=${messages} />`

  return html`
    <${TranscriptItem}
      words=${wordSpans}
      index=${index}
      response=${response} />
  `
}

function TranscriptItem({ words, index, response = "" }) {
  if (words.length === 0) return ""
  return html`
    <p>
      <nav>
        <span class="index">❡${index + 1}</span>
      </nav>
      <div style="display: flex; flex-direction: column; gap: 0.5rem">
        <span>${words}</span>
        <span class="response">${response}</span>
      </div>
    </p>
  `
}

function ChatCompletionSegment({ messages, t0 }) {
  const onError = useCallback((error) => console.error(error), [])

  const { isStreaming, isDone, message } = useChatCompletion({
    model: models[model.value],
    messages,
    temperature: 0,
    onError,
  })

  const displayedText = useTypingEffect(message ? message.content : "")
  const finishedDisplaying = isDone && displayedText === message.content
  const thinking = isStreaming && !finishedDisplaying
  const indicator = thinking ? " 💬" : isDone ? "" : " ⏳"

  useEffect(() => {
    if (message && message.content && finishedDisplaying) {
      emit({ type: "ChatCompletionResult", message: message.content, t0 })
    }
    // scroll to bottom
    window.scrollTo(0, document.body.scrollHeight)
  }, [message.content, finishedDisplaying, t0])

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

function formatDateTimeHuman(date) {
  return date.toLocaleTimeString("en-US", {
    hour: "numeric",
    minute: "2-digit",
  })
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
    language: language.value,
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
    if (readyState === WebSocket.OPEN && !isRecording) {
      console.log("WebSocket open, starting recorder")
      start()
    }

    return () => {
      console.log("WebSocket closed, stopping recorder")
      stop()
    }
  }, [readyState, start, stop])

  return html`<button class="stop" onClick=${stop}></button>`
}

const eventStore = await createEventStore()

show()

window.scrollTo(0, document.body.scrollHeight)

async function emit(payload) {
  const event = await saveEvent(eventStore, payload)
  update(event)
  console.info(payload.type, event)
}

function show() {
  render(html` <${Session} /> `, document.getElementById("app"))
  window.scrollTo(0, document.body.scrollHeight)
}

function update(event) {
  appState.value = reducer(appState.value, event)
  show()
}
