import { Operation, call, useAbortSignal } from "effection"
import GraphemeSplitter from "grapheme-splitter"

export interface Word {
  word: string
  punctuated_word: string
  confidence: number
  start: number
  end: number
}

export interface TranscriptionResult {
  words: Word[]
  paragraphs: {
    paragraphs: {
      sentences: {
        text: string
        start: number
        end: number
      }[]
    }[]
  }
}

export function punctuatedConcatenation(speech: Word[]) {
  return speech.map(({ punctuated_word }) => punctuated_word).join(" ")
}

export function paragraphsToText(x: {
  paragraphs: {
    sentences: {
      text: string
    }[]
  }[]
}) {
  return x.paragraphs
    .map(({ sentences }) => sentences.map(({ text }) => text).join("\n"))
    .join("\n")
}

export function plainConcatenation(speech: Word[]) {
  return speech.map(({ word }) => word).join(" ")
}

export function* hark(
  blobs: Blob[],
  language: string = "en",
): Operation<TranscriptionResult> {
  const formData = new FormData()
  formData.append("file", new Blob(blobs))

  const response = yield* call(
    fetch(`/whisper-deepgram?language=${language}`, {
      method: "POST",
      body: formData,
      signal: yield* useAbortSignal(),
    }),
  )

  if (!response.ok) {
    throw new Error(`HTTP error! status: ${response.status}`)
  }

  const result = yield* call(response.json())
  console.log(result)
  return result.results.channels[0].alternatives[0]
}

const splitter = new GraphemeSplitter()

export function graphemesOf(text: string): string[] {
  return splitter.splitGraphemes(text)
}
