import { Operation, Task, createChannel, main, sleep, spawn } from "effection"

interface Sync<Sign> {
  post: Sign[]
  want: (t: Sign) => boolean
  deny: (t: Sign) => boolean
}

interface Thread<Sign> {
  name: string
  sync: Sync<Sign>
  proc: Generator<Sync<Sign>, void, Sign>
  prio: number
}

function both<V>(x: Set<V> | undefined, y: Set<V> | undefined): Set<V> {
  return (x ?? new Set()).union(y ?? new Set())
}

export function work<Sign>(
  system: Set<Thread<Sign>>,
): Set<Thread<Sign>> | null {
  const postedBy = new Map<Sign, Set<Thread<Sign>>>()
  for (const thread of system) {
    for (const postedSign of thread.sync.post) {
      const set = postedBy.get(postedSign) ?? new Set()
      set.add(thread)
      postedBy.set(postedSign, set)
    }
  }

  const wantedBy = new Map<Sign, Set<Thread<Sign>>>()
  for (const e of postedBy.keys()) {
    const havers = new Set<Thread<Sign>>()

    for (const task of system) {
      if (task.sync.want(e)) {
        havers.add(task)
      }
    }

    if (havers.size > 0) {
      wantedBy.set(e, havers)
    }
  }

  const electedSign = [...system.values()]
    .sort((a, b) => b.prio - a.prio)
    .flatMap((x) => x.sync.post)
    .find((x) => ![...system.values()].some((y) => y.sync.deny(x)))

  if (!electedSign) {
    return null
  } else {
    console.group("pick", electedSign)

    try {
      const affectedThreads = both(
        postedBy.get(electedSign),
        wantedBy.get(electedSign),
      )

      for (const thread of affectedThreads) {
        console.log("next", thread.name)
        const { done, value } = thread.proc.next(electedSign)
        if (done) {
          system.delete(thread)
          console.log("exit", thread.name)
        } else {
          thread.sync = value
        }
      }
    } finally {
      console.groupEnd()
    }

    return system
  }
}

export function sync<Sign>({
  post,
  want,
  deny,
}: Partial<Sync<Sign>>): Sync<Sign> {
  return {
    post: post ?? [],
    want: want ?? (() => false),
    deny: deny ?? (() => false),
  }
}

function noop<T>(name: string): Thread<T> {
  return {
    name,
    proc: (function* () {})(),
    sync: sync({}),
    prio: 0,
  }
}

type Behavior<T> = {
  name: string
  prio: number
  init: () => Generator<Sync<T>, void, T>
}

export function makeThread<T>({ name, prio, init }: Behavior<T>): Thread<T> {
  const proc = init()
  const { done, value: sync } = proc.next()
  return done ? noop(name) : { name, prio, proc, sync }
}

function* system<T, V = void>(
  init: (
    thread: (spec: Behavior<T>) => Operation<void>,
  ) => Operation<Task<V>>,
): Operation<V> {
  const ping = createChannel<void>()
  const starting = new Set<Thread<T>>()

  const task = yield* init(function* (spec) {
    starting.add(makeThread(spec))
    yield* ping.send()
  })

  const pings = yield* ping
  yield* ping.send()

  let threads = new Set<Thread<T>>()

  const task2 = yield* spawn(function* () {
    for (;;) {
      const { done } = yield* pings.next()
      if (done) break

      threads = threads.union(starting)
      starting.clear()

      for (;;) {
        const outcome = work(threads)
        if (outcome === null) {
          break
        } else {
          threads = outcome
        }
      }
    }
  })

  const result = yield* task
  yield* ping.close()
  yield* task2

  return result
}

const syncdemo2 = system<string>(function* (thread) {
  const clock = yield* spawn(function* () {
    for (let i = 0; i < 5; i++) {
      yield* sleep(1000)
      yield* thread({
        name: `second-${i}`,
        prio: 0,
        init: function* () {
          yield sync({ post: ["second"] })
        },
      })
    }
  })

  yield* thread({
    name: "log",
    prio: 1,
    init: function* () {
      for (;;) {
        console.log(yield sync({ want: () => true }))
      }
    },
  })

  yield* thread({
    name: "tick tock",
    prio: 1,
    init: function* () {
      for (;;) {
        yield sync({ post: ["tick"] })
        yield sync({ post: ["tock"] })
      }
    },
  })

  yield* thread({
    name: "interleave",
    prio: 1,
    init: function* () {
      for (;;) {
        yield sync({
          want: (t) => t === "tick",
          deny: (t) => t === "tock",
        })
        yield sync({
          want: (t) => t === "tock",
          deny: (t) => t === "tick",
        })
      }
    },
  })

  yield* thread({
    name: "delay",
    prio: 1,
    init: function* () {
      for (;;) {
        yield sync({
          want: (t) => t === "second",
          deny: (t) => t === "tick" || t === "tock",
        })
        yield sync({ want: (t) => t === "tick" || t === "tock" })
      }
    },
  })

  return clock
})

await main(() => syncdemo2)
