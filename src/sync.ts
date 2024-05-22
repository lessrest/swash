interface Sync<T> {
  have?: T[]
  want?: T[]
  deny?: T[]
}

interface Task<T> {
  name: string
  sync: Sync<T>
  proc: Generator<Sync<T>>
  prio: number
}

function both<V>(x: Set<V> | undefined, y: Set<V> | undefined): Set<V> {
  return (x ?? new Set()).union(y ?? new Set())
}

function also<K, V>(v: V, map: Map<K, Set<V>>, iter?: Iterable<K>) {
  for (const k of iter ?? []) {
    const set = map.get(k) ?? new Set()
    set.add(v)
    map.set(k, set)
  }
  return map
}

export function* work<Sign>(
  jobs: Set<Task<Sign>>,
): Generator<Set<Task<Sign>>, Set<Task<Sign>>> {
  for (;;) {
    const have = new Map<Sign, Set<Task<Sign>>>()
    const want = new Map<Sign, Set<Task<Sign>>>()
    const deny = new Map<Sign, Set<Task<Sign>>>()

    for (const task of jobs) {
      also(task, have, task.sync.have)
      also(task, want, task.sync.want)
      also(task, deny, task.sync.deny)
    }

    const sign = [...jobs.values()]
      .sort((a, b) => b.prio - a.prio)
      .flatMap((x) => x.sync.have ?? [])
      .find((x) => !deny.has(x))

    if (!sign) {
      return jobs
    } else {
      console.group("✱", sign)

      try {
        for (const task of both(have.get(sign), want.get(sign))) {
          console.log("⦿", task.name)
          const { done, value } = task.proc.next()
          if (done) {
            jobs.delete(task)
          } else {
            task.sync = value
          }
        }
      } finally {
        console.groupEnd()
      }

      yield jobs
    }
  }
}

export function* exec<T>(
  init: (
    boot: (task: Task<T>) => void,
    wait: (hope: Promise<T>) => Promise<T>,
  ) => void,
) {
  let jobs = new Set<Task<T>>()
  const hope = new Set<Promise<T>>()

  init(
    (task) => jobs.add(task),
    (promise) => {
      hope.add(promise)
      promise.then(() => hope.delete(promise))
      return promise
    },
  )

  for (;;) {
    const { done, value } = work(jobs).next()
    if (done) {
      if (hope.size > 0) {
        console.log("[waiting for", hope.size, "promises]")
        yield hope
      } else {
        console.log("nothing to wait for")
        return
      }
    } else {
      jobs = value
    }
  }
}

function noop<T>(name: string): Task<T> {
  return {
    name,
    proc: (function* () {})(),
    sync: {},
    prio: 0,
  }
}

export function task<T>(
  name: string,
  prio: number,
  init: () => Generator<Sync<T>>,
): Task<T> {
  const proc = init()
  const { done, value: sync } = proc.next()
  return done ? noop(name) : { name, prio, proc, sync }
}

export function syncdemo1() {
  exec((boot, _wait) => {
    boot(
      task("fill hot", 1, function* () {
        for (;;) {
          yield { want: ["water-low"] }
          yield { have: ["add-hot"] }
          yield { have: ["add-hot"] }
          yield { have: ["add-hot"] }
        }
      }),
    )
    boot(
      task("initially low", 1, function* () {
        yield { have: ["water-low"] }
      }),
    )
    boot(
      task("fill cold", 1, function* () {
        for (;;) {
          yield { want: ["water-low"] }
          yield { have: ["add-cold"] }
          yield { have: ["add-cold"] }
          yield { have: ["add-cold"] }
        }
      }),
    )
    boot(
      task("balance hot/cold", 1, function* () {
        for (;;) {
          yield { want: ["add-hot"], deny: ["add-cold"] }
          yield { want: ["add-cold"], deny: ["add-hot"] }
        }
      }),
    )
  })
}

// demo()
