import { exec, task } from "./sync.ts"

function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

const iter = exec<string>((boot, wait) => {
  async function clock() {
    for (;;) {
      await wait(sleep(1000).then(() => "second"))
    }
  }

  clock()

  boot(
    task<string>("log", 0, function* () {
      for (;;) {
        const sign = yield { want: () => true }
        console.log(sign)
      }
    }),
  )

  boot(
    task<string>("tick tock", 0, function* () {
      for (;;) {
        yield { have: ["tick"] }
        yield { have: ["tock"] }
      }
    }),
  )

  boot(
    task<string>("interleave", 0, function* () {
      for (;;) {
        yield { want: (t) => t === "tick", deny: (t) => t === "tock" }
        yield { want: (t) => t === "tock", deny: (t) => t === "tick" }
      }
    }),
  )

  boot(
    task<string>("delay", 0, function* () {
      for (;;) {
        yield {
          want: (t) => t === "second",
          deny: (t) => t === "tick" || t === "tock",
        }
        yield { want: (t) => t === "tick" || t === "tock" }
      }
    }),
  )
})

async function main() {
  for (;;) {
    const { value, done } = iter.next()
    if (done) return
    await Promise.race([...value])
  }
}

main()
