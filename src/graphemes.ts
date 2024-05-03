import GraphemeSplitter from "grapheme-splitter"

const splitter = new GraphemeSplitter()

export function graphemesOf(text: string): string[] {
  return splitter.splitGraphemes(text)
}
