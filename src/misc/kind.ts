class Kind {
  constructor(public defn: string, public is_a?: Kind) {}
}

function subclass(kind: Kind, defn: string): Kind {
  return new Kind(defn, kind)
}

export const Entity = new Kind("something that exists")

export const Continuant = subclass(Entity, "an entity ")

export const Occurrent = subclass(
  Entity,
  "an entity whose parts are temporal",
)

export const Process = subclass(
  Occurrent,
  "an occurrent that brings about changes",
)

export const PlannedProcess = subclass(
  Process,
  "a process that instantiates a plan",
)

export const Execution = subclass(
  PlannedProcess,
  "a process whereby a computer realizes software",
)

export const ConcurrentExecution = subclass(
  Execution,
  "a execution with concurrent execution parts",
)

export const SequentialExecution = subclass(
  Execution,
  "a execution without concurrent execution parts",
)
