import { Operation, Task, call, createContext, spawn } from "effection"
import { html } from "./html.ts"
import { grow, nest, quiz } from "./nest.ts"

export const Breadcrumb = createContext<string[]>("Breadcrumb", [])

export function* getBreadcrumb(): Operation<string[]> {
  return (yield* Breadcrumb.get()) || ["[no task]"]
}

class Console {
  static stack: string[] = []

  static update(next: string[], symbol: string = "") {
    const index = this.stack.findIndex((item, i) => item !== next[i])
    const keep = index === -1 ? this.stack.length : index

    for (const _ of this.stack.slice(keep)) {
      console.groupEnd()
    }

    for (const crumb of next.slice(keep)) {
      console.group("✱ %c%s %s", "color: #aa5", crumb, symbol)
    }

    this.stack = [...next]
  }

  static info(...args: unknown[]) {
    console.log("❡", ...args)
  }

  static syslog(...args: unknown[]) {
    console.log("◉", ...args)
  }
}

export function* info<T extends unknown[]>(...args: T) {
  Console.update(yield* getBreadcrumb())
  Console.info(...(yield* formatArgs(args)))
}

export function* syslog<T extends unknown[]>(...args: T) {
  Console.update(yield* getBreadcrumb())
  Console.syslog(...(yield* formatArgs(args)))
}

export const Dawn = createContext<Date>("Epoch")

export function* dawn() {
  yield* Dawn.set(new Date())
}

function* formatArgs(args: unknown[]): Operation<unknown[]> {
  const formattedArgs = []
  for (const arg of args) {
    if (arg instanceof Date) {
      const epoch = yield* Dawn.get()
      if (epoch && arg.getTime() !== epoch.getTime()) {
        const relativeTime = (
          (arg.getTime() - epoch.getTime()) /
          1000
        ).toFixed(1)
        formattedArgs.push(`T+${relativeTime}s`)
      } else {
        formattedArgs.push(arg.toLocaleString())
      }
    } else {
      formattedArgs.push(arg)
    }
  }
  return formattedArgs
}

export function* pushTaskName(name: string) {
  const breadcrumb = [...(yield* getBreadcrumb()), name]
  yield* Breadcrumb.set(breadcrumb)
  Console.update(breadcrumb)
}

export function* task<T>(
  name: string,
  fn: () => Operation<T>,
): Operation<Task<T>> {
  return yield* spawn(function* () {
    const node = yield* nest(
      html("task", { data: { name, state: "started" } }),
    )
    yield* grow(html("header", {}, name))
    yield* nest(html("main"))
    yield* pushTaskName(name)
    try {
      yield* syslog("started at", new Date())
      const x = yield* fn()
      node.dataset.state = "finished"
      yield* syslog("finished at", new Date())
      return x
    } catch (err) {
      yield* syslog("failed at", new Date(), err)
      node.dataset.state = "failed"
      throw err
    }
  })
}

export function* conf(name: string): Operation<string> {
  const value = localStorage.getItem(name)
  if (value) {
    return value
  } else {
    const input = yield* quiz(name)
    if (input) {
      localStorage.setItem(name, input)
      return input
    } else {
      throw new Error(`Configuration value ${name} not found`)
    }
  }
}

export function* redo<T>(
  fn: () => Operation<T>,
  options: {
    many?: number
    wait?: number
    peak?: number
    frob?: boolean
  } = {},
): Operation<T> {
  const { many = 5, wait = 100, peak = 5000, frob = true } = options

  let n = 0
  let t = wait

  return yield* call(function* () {
    //    const node = yield* nest(html("retry", {}, `${n}/${many}`))
    try {
      while (true) {
        try {
          n++
          return yield* fn()
        } catch (err) {
          if (n >= many) {
            throw err
          }

          yield* wait(t)

          t = Math.min(t * 2, peak)
          if (frob) {
            t = t * (0.75 + Math.random() * 0.5)
          }
        }
      }
    } finally {
      //      node.remove()
    }
  })
}
