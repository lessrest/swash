import { html } from "./html.ts"

class TypeWriter extends HTMLElement {
  limit = 0
  range = new Range()
  timer?: number
  snitch = new MutationObserver(() => {
    this.update()
    if (!this.timer) this.proceed()
  })

  connectedCallback() {
    this.range.selectNodeContents(this)

    CSS.highlights.set(
      "hidden",
      (CSS.highlights.get("hidden") ?? new Highlight()).add(this.range),
    )

    this.snitch.observe(this, {
      childList: true,
      subtree: true,
      characterData: true,
    })

    this.proceed()
  }

  disconnectedCallback() {
    this.snitch.disconnect()
    CSS.highlights.get("hidden")?.delete(this.range)
    clearTimeout(this.timer)
  }

  update() {
    const walk = document.createTreeWalker(this, NodeFilter.SHOW_TEXT)
    let node: Text | null = null
    let limit = this.limit

    while (walk.nextNode()) {
      node = walk.currentNode as Text
      const { length } = node.data.slice(0, limit)
      limit -= length
      if (limit <= 0) {
        this.range.setStart(node, length)
        break
      }
    }

    if (limit > 0) this.range.setStart(this, 0)

    this.range.setEndAfter(this)
  }

  proceed() {
    console.log({ range: this.range.toString(), inner: this.innerText })
    if (this.range.toString().trim() === "") {
      this.timer = undefined
      return
    }

    this.limit = Math.min(this.limit + 1, this.innerText.length)
    this.update()

    const delay = adjustSpeed(this.innerText.length, this.range.toString())
    this.timer = setTimeout(() => this.proceed(), 1000 / delay)
  }
}

function adjustSpeed(length: number, suffix: string) {
  const maxSpeed = 80
  const minSpeed = 30
  const speedRange = maxSpeed - minSpeed
  const speedFactor = 1 - suffix.length / length
  const base = Math.round(minSpeed + speedRange * speedFactor ** 2)

  return delayForGrapheme(suffix[0], base)

  function delayForGrapheme(grapheme: string, baseDelay: number) {
    const factors: Record<string, number> = {
      // TODO: justify these arbitrary numbers with pseudoscience
      " ": 3,
      "–": 7,
      ",": 8,
      ";": 8,
      ":": 9,
      ".": 10,
      "—": 12,
      "!": 15,
      "?": 15,
      "\n": 20,
    }
    return baseDelay * (factors[grapheme] ?? 1) * 0.8
  }
}
customElements.define("type-writer", TypeWriter)
document.addEventListener("DOMContentLoaded", () => {
  document.body.append(
    html("style", {}, `::highlight(hidden) { color: transparent }`),
  )
})
