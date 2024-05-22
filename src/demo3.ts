import { exec, task } from "./sync.ts"

function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

const iter = exec((boot, wait) => {
  async function clock() {
    for (;;) {
      await wait(sleep(1000))
      boot(
        task("second", 0, function* () {
          yield { have: ["second"] }
        }),
      )
    }
  }

  clock()

  boot(
    task("tick tock", 0, function* () {
      for (;;) {
        yield { have: ["tick"] }
        yield { have: ["tock"] }
      }
    }),
  )

  boot(
    task("interleave", 0, function* () {
      for (;;) {
        yield { want: ["tick"], deny: ["tock"] }
        yield { want: ["tock"], deny: ["tick"] }
      }
    }),
  )

  boot(
    task("delay", 0, function* () {
      for (;;) {
        yield { want: ["second"], deny: ["tick", "tock"] }
        yield { want: ["tick", "tock"] }
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
