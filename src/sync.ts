interface Sync<T> {
  have?: T[]
  want?: T[]
  deny?: T[]
}

interface Task<T> {
  name: string
  sync: Sync<T>
  proc: Generator<Sync<T>>
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

    const sign = [...have.keys()].find((x) => !deny.has(x))

    if (!sign) {
      return jobs
    } else {
      console.log("✱", sign)

      for (const task of both(have.get(sign), want.get(sign))) {
        const { done, value } = task.proc.next()
        if (done) {
          jobs.delete(task)
        } else {
          task.sync = value
        }
      }

      yield jobs
    }
  }
}

export function exec<T>(tasks: Iterable<Task<T>>) {
  let x = new Set(tasks)
  for (;;) {
    const { done, value } = work(x).next()
    if (done) {
      console.log("◼︎", value)
      return
    } else {
      console.log("✅", value)
      x = value
    }
  }
}

function noop<T>(name: string): Task<T> {
  return {
    name,
    proc: (function* () {})(),
    sync: {},
  }
}

export function task<T>(
  name: string,
  init: () => Generator<Sync<T>>,
): Task<T> {
  const proc = init()
  const { done, value: sync } = proc.next()
  return done ? noop(name) : { name, proc, sync }
}

function demo() {
  exec([
    task("fill hot", function* () {
      for (;;) {
        yield { want: ["water-low"] }
        yield { have: ["add-hot"] }
        yield { have: ["add-hot"] }
        yield { have: ["add-hot"] }
      }
    }),
    task("initially low", function* () {
      yield { have: ["water-low"] }
    }),
    task("fill cold", function* () {
      for (;;) {
        yield { want: ["water-low"] }
        yield { have: ["add-cold"] }
        yield { have: ["add-cold"] }
        yield { have: ["add-cold"] }
      }
    }),
    task("balance hot/cold", function* () {
      for (;;) {
        yield { want: ["add-hot"], deny: ["add-cold"] }
        yield { want: ["add-cold"], deny: ["add-hot"] }
      }
    }),
  ])
}

demo()
