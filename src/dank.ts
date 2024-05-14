import { into } from "./nest.ts"
import { dawn } from "./task.ts"

export function* dank() {
  yield* dawn()
  yield* into(document.body)
}
