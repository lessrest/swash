interface Sync<T> {
  have?: T[]
  want?: (t: T) => boolean
  deny?: (t: T) => boolean
}

interface Task<T> {
  name: string
  sync: Sync<T>
  proc: Generator<Sync<T>, void, T>
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

    for (const task of jobs) {
      also(task, have, task.sync.have)
    }

    for (const sign of have.keys()) {
      const tasks = new Set<Task<Sign>>()
      for (const task of jobs) {
        if (task.sync.want?.(sign)) {
          tasks.add(task)
        }
      }
      if (tasks.size > 0) {
        want.set(sign, tasks)
      }
    }

    const sign = [...jobs.values()]
      .sort((a, b) => b.prio - a.prio)
      .flatMap((x) => x.sync.have ?? [])
      .find((x) => ![...jobs.values()].some((y) => y.sync.deny?.(x)))

    if (!sign) {
      return jobs
    } else {
      console.group("✱", sign)

      try {
        for (const task of both(have.get(sign), want.get(sign))) {
          console.log("⦿", task.name)
          const { done, value } = task.proc.next(sign)
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
      promise.then((sign) => {
        hope.delete(promise)
        jobs.add(
          task("resolve", 0, function* () {
            yield { have: [sign] }
          }),
        )
      })
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
  init: () => Generator<Sync<T>, void, T>,
): Task<T> {
  const proc = init()
  const { done, value: sync } = proc.next()
  return done ? noop(name) : { name, prio, proc, sync }
}

export function syncdemo1() {
  exec<string>((boot, _wait) => {
    boot(
      task<string>("fill hot", 1, function* () {
        for (;;) {
          yield { want: (t) => t === "water-low" }
          yield { have: ["add-hot"] }
          yield { have: ["add-hot"] }
          yield { have: ["add-hot"] }
        }
      }),
    )
    boot(
      task<string>("initially low", 1, function* () {
        yield { have: ["water-low"] }
      }),
    )
    boot(
      task<string>("fill cold", 1, function* () {
        for (;;) {
          yield { want: (t) => t === "water-low" }
          yield { have: ["add-cold"] }
          yield { have: ["add-cold"] }
          yield { have: ["add-cold"] }
        }
      }),
    )
    boot(
      task<string>("balance hot/cold", 1, function* () {
        for (;;) {
          yield {
            want: (t) => t === "add-hot",
            deny: (t) => t === "add-cold",
          }
          yield {
            want: (t) => t === "add-cold",
            deny: (t) => t === "add-hot",
          }
        }
      }),
    )
  }).next()
}

// syncdemo1()
