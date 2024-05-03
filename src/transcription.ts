export interface SpokenWord {
  word: string
  punctuated_word: string
  confidence: number
  start: number
  end: number
}

export interface TranscriptionResult {
  words: SpokenWord[]
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

export function* transcribe(
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
