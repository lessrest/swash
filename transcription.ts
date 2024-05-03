export interface SpokenWord {
  word: string
  punctuated_word: string
  confidence: number
  start: number
  end: number
}

export function punctuatedConcatenation(speech: SpokenWord[]) {
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
    .map(({ sentences }) => sentences.map(({ text }) => text).join(" "))
    .join("\n\n")
}

export function plainConcatenation(speech: SpokenWord[]) {
  return speech.map(({ word }) => word).join(" ")
}
