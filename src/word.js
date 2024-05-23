import * as wordnet from "wordnet"

await wordnet.init()

const words4 = (await wordnet.list())
  .filter((word) => word.length === 4)
  .sort()

const defns = (
  await Promise.all(
    words4.map((word) => wordnet.lookup(word).then((x) => [word, x])),
  )
).flatMap(([word, defns]) =>
  defns.map((defn) => ({
    word,
    text: defn.glossary,
    kind: defn.meta.synsetType,
  })),
)

const json = JSON.stringify(defns, null, 2)

console.log(`
export type Word = {
  word: string
  text: string
  kind: string
}
export const words: Word[] = ${json}
`)
