import { openDB } from "idb"

const storeName = "events.2"

export async function createEventStore() {
  const eventStore = await openDB("swa.sh", 3, {
    upgrade: (db, oldVersion, newVersion, transaction) => {
      db.createObjectStore(storeName, {
        keyPath: "sequenceNumber",
        autoIncrement: true,
      })
    },
  })

  return eventStore
}

export async function saveEvent(eventStore, key, payload) {
  const events = await eventStore.getAll(storeName)
  const lastEvent = events[events.length - 1]
  const seq = lastEvent ? lastEvent.payload.seq + 1 : 0

  const response = await fetch("/append-event", {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: `key=${encodeURIComponent(key)}&seq=${seq}&payload=${encodeURIComponent(JSON.stringify(payload))}`,
  })

  if (response.ok) {
    const savedEvent = {
      timestamp: Date.now(),
      payload: { ...payload, seq },
    }
    await eventStore.add(storeName, savedEvent)
    return savedEvent
  } else {
    throw new Error("Failed to save event")
  }
}

export async function getAllEvents(eventStore) {
  return await eventStore.getAll(storeName)
}
const EVENT_KEY = "eventKey"

function getEventKey() {
  let eventKey = localStorage.getItem(EVENT_KEY)
  if (!eventKey) {
    eventKey = crypto.randomUUID()
    localStorage.setItem(EVENT_KEY, eventKey)
  }
  return eventKey
}
